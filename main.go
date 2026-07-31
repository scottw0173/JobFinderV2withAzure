package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	Logger *slog.Logger
	Client *http.Client // shared, cloud-agnostic (ATS fetchers)

	Store   Store
	Config  ConfigSource
	Secrets Secrets
	Scorer  Scorer // AWS: the one Gemini scorer, dispatched directly

	// Scorers routes by protocol (CLAUDE.md §7), keyed by ModelConfig.Protocol.
	// Azure-only; nil on AWS, which has no per-model protocol to route on.
	Scorers map[string]Scorer

	spreadsheetID string // Sheets export stays outside the four interfaces

	cloudProvider string

	// InstructionsVersion is a short content hash of instructions.md,
	// computed once at wire time (CLAUDE.md §10). Set by wireAzure only;
	// stays "" on AWS.
	InstructionsVersion string
}

var app *App

func main() {
	provider := os.Getenv("CLOUD_PROVIDER")
	if provider == "" {
		provider = "aws"
	}

	logger := initLogger()

	app = &App{
		Logger:        logger,
		Client:        &http.Client{Timeout: 60 * time.Second},
		cloudProvider: provider,
	}

	ctx := context.Background()

	var err error
	switch provider {
	case "aws":
		err = wireAWS(ctx, app)
	case "azure":
		err = wireAzure(ctx, app)
	default:
		log.Fatalf("unknown CLOUD_PROVIDER %q", provider)
	}
	if err != nil {
		log.Fatalf("error wiring %s: %v", provider, err)
	}

	// AWS_LAMBDA_RUNTIME_API is set by the Lambda platform itself, so this
	// detects "am I running inside Lambda" independent of CLOUD_PROVIDER.
	// Lambda's provided.al2023 custom runtime requires the process to speak
	// the Runtime API loop (lambda.Start handles that); everywhere else
	// (Azure Container Apps Jobs, local dev) this is a single plain run.
	if os.Getenv("AWS_LAMBDA_RUNTIME_API") != "" {
		lambda.Start(func(ctx context.Context) error {
			if err := handler(ctx); err != nil {
				app.Logger.Error("AWS run failed", errAttr(err))
				return err
			}
			return nil
		})
		return
	}

	if err := handler(ctx); err != nil {
		app.Logger.Error("Azure run failed", errAttr(err))
		os.Exit(1)
	}
}

// wireAWS builds the AWS-backed Store/ConfigSource/Secrets/Scorer and wires
// them onto app, preserving the exact env vars and startup order the AWS
// Lambda deployment has always used.
func wireAWS(ctx context.Context, app *App) error {
	dynamoTableName := os.Getenv("DYNAMOTABLE")
	if dynamoTableName == "" {
		return traceErrorf("DYNAMOTABLE env var not set")
	}
	s3Region := os.Getenv("S3REGION")
	if s3Region == "" {
		return traceErrorf("S3REGION env var not set")
	}
	geminimodel := os.Getenv("GEMINIMODEL")
	if geminimodel == "" {
		return traceErrorf("GEMINIMODEL env var not set")
	}
	s3config := os.Getenv("S3CONFIG")
	if s3config == "" {
		return traceErrorf("S3CONFIG env var not set")
	}
	app.spreadsheetID = os.Getenv("SPREADSHEETID")

	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(s3Region))
	if err != nil {
		return wrapErr("configuring aws sdk", err)
	}

	dynamoClient := dynamodb.NewFromConfig(awsCfg)
	s3client := s3.NewFromConfig(awsCfg)
	ssmClient := ssm.NewFromConfig(awsCfg)

	app.Store = newAWSStore(app.Logger, dynamoClient, dynamoTableName)
	app.Config = newAWSConfigSource(s3client, s3config, geminimodel)
	app.Secrets = newAWSSecrets(ssmClient)

	geminikey, err := app.Secrets.Fetch(ctx, os.Getenv("GEMINIAPIKEY"))
	if err != nil {
		return wrapErr("fetching gemini key", err)
	}

	app.Logger.Info("aws app and logger initialized", "time", time.Now())
	instructions, err := app.Config.File(ctx, "instructions.md")
	if err != nil {
		return wrapErr("getting instructions", err)
	}
	app.Scorer = newGeminiScorer(app.Logger, app.Client, geminikey, instructions)
	return nil
}

// wireAzure builds the Azure-side impls. Most are stubs until their
// corresponding later migration steps land (see store_azure.go,
// secrets_azure.go, scorer_openai.go); ConfigSource is functional now since
// handler() needs it to succeed at startup.
func wireAzure(ctx context.Context, app *App) error {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		return traceErrorf("POSTGRES_DSN env var not set")
	}
	pgCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return wrapErr("parsing postgres dsn", err)
	}
	cred, err := newAzureCredential()
	if err != nil {
		return wrapErr("constructing azure credential", err)
	}
	if !dsnHasPassword(pgCfg) {
		// No password in the DSN (Bicep's AAD-only shape, postgres.bicep's
		// passwordAuth: 'Disabled') - fetch one via managed identity per
		// connection attempt (CLAUDE.md §9). Local dev's DSN carries a real
		// password and skips this entirely, unchanged.
		pgCfg.BeforeConnect = newBeforeConnectHook(cred, postgresAADScope)
	}
	pool, err := pgxpool.NewWithConfig(ctx, pgCfg)
	if err != nil {
		return wrapErr("connecting to postgres", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return wrapErr("pinging postgres", err)
	}

	app.Store = newAzureStore(app.Logger, pool)
	configSource, err := newAzureConfigSource(cred)
	if err != nil {
		return wrapErr("constructing azure config source", err)
	}
	app.Config = configSource
	app.Secrets = newAzureSecrets()

	app.Logger.Info("app and logger initialized", "time", time.Now())
	instructions, err := app.Config.File(ctx, "instructions.md")
	if err != nil {
		return wrapErr("getting instructions", err)
	}
	// A content hash, not a human-typed label (CLAUDE.md §10): guarantees it
	// can't drift from what was actually sent, since editing instructions.md
	// changes the hash automatically - no version bump to remember.
	instructionsSum := sha256.Sum256(instructions)
	app.InstructionsVersion = hex.EncodeToString(instructionsSum[:])[:12]

	apiKey := os.Getenv("AZURE_OPENAI_API_KEY") // empty is expected/fine for local Ollama; real key lands in step 7

	// Dedicated client, not app.Client: that one's tuned for fast ATS API
	// fetches (60s timeout). Local CPU inference on a 5-job batch can
	// legitimately take longer than that; a hung real endpoint should still
	// fail eventually, just not on the ATS fetchers' clock.
	scorerClient := &http.Client{Timeout: 5 * time.Minute}
	// Routed by protocol (CLAUDE.md §7), not assigned to app.Scorer directly:
	// BaseURL is per-model now (§6/§12), so there's no single global endpoint
	// to construct one scorer against. Only "openai" is populated today -
	// every model in the current panel is OpenAI-compatible (§12); a second
	// protocol is a config-only addition of another map entry once a model
	// actually needs one.
	app.Scorers = map[string]Scorer{
		"openai": newOpenAIScorer(app.Logger, scorerClient, apiKey, cred, instructions),
	}
	return nil
}

func handler(ctx context.Context) error {
	all, err := collect(ctx, app)
	if err != nil {
		wrapped := wrapErr("error collecting jobs", err)
		app.Logger.Error("cannot collect jobs", errAttr(wrapped))
		return wrapped
	}

	seenJobs, err := app.Store.SeenJobs(ctx)
	if err != nil {
		app.Logger.Error("cannot read results from store", errAttr(err))
	}
	seenSet := seenJobKeySet(seenJobs)
	var fresh []Job
	app.Logger.Debug("checking again seen set begins", "time", time.Now())
	for _, job := range all {
		if !seenSet[job.createCompositeKey()] {
			fresh = append(fresh, job)
		}
	}
	liveKeys := make(map[string]struct{}, len(all))
	for _, job := range all {
		liveKeys[job.createCompositeKey()] = struct{}{}
	}
	now := time.Now()
	cutoff := now.Add(-48 * time.Hour)
	var toBump, aged []SeenJob
	for _, item := range seenJobs {
		if _, live := liveKeys[item.compositeKey()]; live {
			toBump = append(toBump, item)
		} else if item.LastSeen.Before(cutoff) && !item.HasApplied {
			aged = append(aged, item)
		}
	}
	app.Logger.Info("updating 'last_seen' for active entries", "count", len(toBump))
	if err := app.Store.BumpLastSeen(ctx, toBump, now); err != nil {
		app.Logger.Error("cannot update last seen", errAttr(err))
	}
	app.Logger.Info("deleting aged out entries", "count", len(aged))
	if _, err := app.Store.DeleteAged(ctx, aged); err != nil {
		app.Logger.Error("cannot delete aged entries", errAttr(err))
	}
	app.Logger.Debug("checking again seen set ends", "time", time.Now())

	filter, err := LoadKeywordFilter(ctx, app)
	if err != nil {
		wrapped := wrapErr("error loading filter file", err)
		app.Logger.Error("cannot load filtering data ", errAttr(wrapped))
		return wrapped
	}

	// Rescore policy is configuration, not forked code: AWS skips jobs it has
	// already scored (fresh only); Azure re-scores every currently-live job
	// each run to measure cross-model/temporal drift.
	candidates := fresh
	if app.Config.RescoreEveryRun() {
		candidates = all
	}
	matched := filterJobs(candidates, filter)
	app.Logger.Info("matched jobs", "count", len(matched))

	// Fixed score-stratified 30-job panel (CLAUDE.md): scoring the same
	// curated set every run, instead of the full post-filter set fresh each
	// time, lets score drift over the measurement window be attributed to
	// the model rather than to job-set churn. Azure-only; AWS's PanelEnabled
	// is hardcoded false, so this branch never executes there.
	if app.cloudProvider == "azure" && app.Config.PanelEnabled() {
		panelJobs, err := loadOrBuildPanel(ctx, app, matched)
		if err != nil {
			wrapped := wrapErr("error resolving job panel", err)
			app.Logger.Error("cannot resolve panel", errAttr(wrapped))
			return wrapped
		}
		matched = panelJobs
		app.Logger.Info("using fixed panel for scoring", "count", len(matched))
	}

	models, err := app.Config.Models(ctx)
	if err != nil {
		wrapped := wrapErr("error loading model list", err)
		app.Logger.Error("cannot load model list", errAttr(wrapped))
		return wrapped
	}

	// Only "main" is wired end to end (CLAUDE.md §2); refuse anything else
	// fast rather than silently producing main-shaped data mislabeled as a
	// mode ("floor") that has no repeat-scoring logic behind it yet.
	if mode := app.Config.RunMode(); mode != "main" {
		return traceErrorf("run mode %q not implemented (only \"main\" is wired; floor is a later edition)", mode)
	}

	// Run-level, not per-model: read once,
	// applied to every model in this run so
	// model-vs-condition stays identifiable.
	batchSize := app.Config.BatchSize()
	temperature := app.Config.Temperature()

	// Multi-contributor identity (CLAUDE.md §10): without it, person-effects
	// and model-effects are inseparable once data from more than one
	// contributor exists. Azure-only - AWS's DynamoDB store has no columns
	// for this, and there's no non-fabricated equivalent to invent for it.
	// Checked before any scoring happens so a misconfigured run fails fast
	// rather than wasting API spend and then refusing to store the results.
	var contributorID, resumeID, configID string
	if app.cloudProvider == "azure" {
		contributorID = app.Config.ContributorID()
		resumeID = app.Config.ResumeID()
		configID = app.Config.ConfigID()
		if contributorID == "" || resumeID == "" || configID == "" {
			return traceErrorf("missing contributor/resume/config identity - set AZURE_CONTRIBUTOR_ID, AZURE_RESUME_ID, AZURE_CONFIG_ID (CLAUDE.md §10)")
		}
	}

	var events []ScoringEvent
	for _, model := range models {
		// A model with no configured TPM/RPM has no known rate limit to
		// throttle against - refuse to score it rather than run unthrottled
		// against a real endpoint. This is expected to
		// skip every defaultAzureModels entry until launch-day values land.
		if model.TPM <= 0 || model.RPM <= 0 {
			app.Logger.Error("model missing TPM/RPM, refusing to score",
				"model", model.Name, "tpm", model.TPM, "rpm", model.RPM)
			continue
		}

		// Resolve the scorer for this model by protocol.
		// Gated on app.Scorers != nil - the actual precondition for routing
		// being in play - rather than cloudProvider, so this stays
		// self-contained to the mechanism it protects. AWS never sets
		// Scorers, so app.Scorer (the direct Gemini dispatch) is used as-is.
		scorer := app.Scorer
		if app.Scorers != nil {
			s, ok := app.Scorers[model.Protocol]
			if !ok {
				app.Logger.Error("no scorer registered for protocol, refusing to score",
					"model", model.Name, "protocol", model.Protocol)
				continue
			}
			if model.BaseURL == "" {
				app.Logger.Error("model missing BaseURL, refusing to score", "model", model.Name)
				continue
			}
			scorer = s
		}

		// Fresh throttle per model: each model has its own independent
		// TPM/RPM quota, derated to 75% by newModelThrottle.
		throttle, limiter := newModelThrottle(model)
		for i := 0; i < len(matched); i += batchSize {
			<-limiter.C // RPM limiter
			end := min(i+batchSize, len(matched))

			tokenEstimate := 3000.0 // initial for estimated prompt/resume tokens
			var descChars int
			for _, j := range matched[i:end] {
				descChars += len(j.Description)
			}
			tokenEstimate += float64(descChars) / float64(1.75)

			if err := throttle.reserve(ctx, tokenEstimate); err != nil {
				break
			}
			results, usage, err := app.scoreBatchRetry(ctx, scorer, matched[i:end], model, temperature)
			if err != nil {
				app.Logger.Error("aborting run, batch failed", "start", i, "model", model.Name, errAttr(err))
				break
			}
			if usage.Total > 0 {
				app.Logger.Info("token estimate calibration",
					"est", tokenEstimate,
					"actual_total", usage.Total,
					"length of descriptions", descChars,
					"chars_per_token", float64(descChars)/usage.Total,
					"est_ratio", tokenEstimate/usage.Total)
			} else {
				app.Logger.Warn("zero token count on success path- skipping calibration", "start", i)
			}
			throttle.record(usage.Total)
			events = append(events, zipScoreEvents(app, matched[i:end], results)...)
		}
		limiter.Stop()
	}
	if err := app.Store.RecordScores(ctx, events, contributorID, resumeID, configID, app.InstructionsVersion); err != nil {
		app.Logger.Error("cannot write results to store", errAttr(err))
	} else {
		app.Logger.Info("results successfully written to store")
	}
	rows, err := app.Store.ExportRows(ctx)
	if err != nil {
		app.Logger.Error("cannot gather jobs for export", errAttr(err))
	} else if err := exportToSheet(ctx, app, rows); err != nil {
		app.Logger.Error("cannot export jobs to sheet", errAttr(err))
	}
	return nil
}
