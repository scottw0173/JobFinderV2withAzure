package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// azureStore is the Postgres Store impl, backed by db/schema.sql's
// three-table design: jobs (one row per posting, liveness bookkeeping,
// write-once content), scoring_calls (one row per HTTP scoring call, holding
// call-level facts like batch_size and token usage), and scoring_events (one
// row per job scored within a call, FK'd to both). Unlike AWS's DynamoDB impl
// (one item per job, overwritten with the latest score), this preserves
// every scoring event.
type azureStore struct {
	logger *slog.Logger
	pool   *pgxpool.Pool
}

func newAzureStore(logger *slog.Logger, pool *pgxpool.Pool) *azureStore {
	return &azureStore{logger: logger, pool: pool}
}

// scoringCall is one reconstructed ScoreBatch call: every job it scored,
// still grouped together.
type scoringCall struct {
	model       string
	deployment  string
	batchIndex  int
	scoredAt    time.Time
	temperature float64
	usage       Usage
	events      []ScoringEvent
}

// groupByCall reconstructs which ScoringEvents came from the same
// ScoreBatch HTTP call. ScoringEvent carries no call id of its own -
// scorer.go's shape predates the calls/events split - but openaiScorer.
// ScoreBatch stamps every result from one call with the same model.Name and
// a single time.Now() call (see scorer_openai.go), so (Model, ScoredAt) is a
// reliable reconstruction key in practice. Wire an explicit CallID through
// ScoreResult instead if that assumption ever breaks.
func groupByCall(events []ScoringEvent) []scoringCall {
	type key struct {
		model    string
		scoredAt int64
	}
	idx := make(map[key]int)
	var calls []scoringCall
	for _, e := range events {
		k := key{e.Result.Model, e.Result.ScoredAt.UnixNano()}
		i, ok := idx[k]
		if !ok {
			i = len(calls)
			idx[k] = i
			calls = append(calls, scoringCall{
				model:       e.Result.Model,
				deployment:  e.Result.Deployment,
				batchIndex:  e.BatchIndex,
				scoredAt:    e.Result.ScoredAt,
				temperature: e.Result.Temperature,
				usage:       e.Result.Usage,
			})
		}
		calls[i].events = append(calls[i].events, e)
	}
	return calls
}

// RecordScores writes one scoring_calls row per reconstructed call plus one
// scoring_events row per job scored in that call, each inside its own short
// transaction - this mirrors AWS's per-item try/log/continue tolerance (a
// bad call is rolled back and logged, the rest of the run proceeds) at the
// finest granularity the new schema allows: scoring_events rows within a
// call share a single call_id FK, so the call is the atomic unit, not the
// individual event.
//
// This is the TRUE persistence point (the events slice handler() builds up
// is just an in-process accumulator; nothing lands in Postgres until here) -
// so this is where rows_written and the run-end expected/actual tally are
// logged, per the observability task. It also fixes a real bug: this used to
// always return nil even when every call failed to persist, which made
// handler()'s success/failure branch a lie. Now it returns a non-nil error
// whenever at least one call failed, aggregating the count.
func (s *azureStore) RecordScores(ctx context.Context, events []ScoringEvent, contributorID, resumeID, configID, instructionsVersion string) error {
	expectedTotal := len(events)
	actualTotal := 0
	perModel := map[string]int{}
	calls := groupByCall(events)
	failedCalls := 0

	for _, call := range calls {
		if err := s.recordCall(ctx, call, contributorID, resumeID, configID, instructionsVersion); err != nil {
			failedCalls++
			s.logger.Error("failed to record scoring call", errAttr(err),
				slog.String("model", call.model), slog.Int("batch_index", call.batchIndex),
				slog.Int("batch_size", len(call.events)), slog.String("table", "scoring_calls/scoring_events"),
				slog.String("outcome", "failed"), slog.String("error_class", errorClass(err)))
			continue
		}
		actualTotal += len(call.events)
		perModel[call.model] += len(call.events)
		s.logger.Info("scoring call persisted",
			slog.String("model", call.model), slog.Int("batch_index", call.batchIndex),
			slog.Int("batch_size", len(call.events)), slog.Int("rows_written", len(call.events)),
			slog.String("table", "scoring_events"), slog.String("outcome", "ok"))
	}

	s.logger.Info("run end",
		slog.Int("expected_total", expectedTotal), slog.Int("actual_total", actualTotal),
		slog.Any("per_model", perModel))

	if failedCalls > 0 {
		return traceErrorf("%d of %d scoring calls failed to persist", failedCalls, len(calls))
	}
	return nil
}

func (s *azureStore) recordCall(ctx context.Context, call scoringCall, contributorID, resumeID, configID, instructionsVersion string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return wrapErr("begin tx", err)
	}
	defer tx.Rollback(ctx) // no-op after Commit

	// deployment and usage are call-level facts, duplicated
	// onto every event in the batch by the scorer and taken-first here in
	// groupByCall - same pattern already used for temperature. The
	// itemized Usage fields are *int64, nil when the provider's response
	// didn't itemize them; pgx passes a nil pointer through as SQL NULL,
	// same mechanism relied on for EVScore. usage_raw is nullable JSONB, so
	// an empty Raw blob is passed through as untyped nil -> SQL NULL rather
	// than an empty string.
	//
	// contributorID/resumeID/configID/instructionsVersion are
	// constant for the whole run, unlike temperature (call.temperature),
	// which genuinely varies per reconstructed call - so these are passed
	// straight through as literal args rather than threaded through
	// ScoreResult/groupByCall.
	var usageRaw any
	if len(call.usage.Raw) > 0 {
		usageRaw = string(call.usage.Raw)
	}
	var callID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO scoring_calls (
			model, deployment, batch_size, temperature_sent, run_kind, scored_at,
			input_uncached, cached_read, cache_write, output_tokens, reasoning_tokens, usage_raw,
			contributor_id, resume_id, config_id, instructions_version
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING call_id
	`, call.model, call.deployment, len(call.events), call.temperature, "main", call.scoredAt,
		call.usage.InputUncached, call.usage.CacheRead, call.usage.CacheWrite, call.usage.Output, call.usage.Reasoning, usageRaw,
		contributorID, resumeID, configID, instructionsVersion).Scan(&callID)
	if err != nil {
		return wrapErr("insert scoring_calls row", err)
	}

	for _, e := range call.events {
		if err := s.recordEvent(ctx, tx, callID, e); err != nil {
			return err
		}
	}

	return wrapErrIfSet("commit tx", tx.Commit(ctx))
}

func (s *azureStore) recordEvent(ctx context.Context, tx pgx.Tx, callID int64, e ScoringEvent) error {
	payload, err := json.Marshal(e.Job)
	if err != nil {
		return wrapErr("marshal job payload", err)
	}

	stablekey := e.Job.createStableKey()
	compositeKey := e.Job.createCompositeKey()
	postedAt := time.Unix(e.Job.PostedAt, 0)
	descChars := len(e.Job.Description)

	_, err = tx.Exec(ctx, `
		INSERT INTO jobs (composite_key, stablekey, posted_at, source, title, company, url, payload, description_chars, payload_chars)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (composite_key) DO UPDATE SET last_seen = now()
	`, compositeKey, stablekey, postedAt, e.Job.Source, e.Job.Title, e.Job.Company, e.Job.URL,
		string(payload), descChars, len(payload))
	// Job content is write-once above (only last_seen updates on conflict):
	// composite_key already encodes stablekey+posted_at, so a row that
	// matches an existing composite_key is necessarily a re-scrape of the
	// same posting - its content can't have legitimately changed.
	if err != nil {
		return wrapErr("upsert jobs row", err)
	}

	var logprobs any
	if len(e.Result.Logprobs) > 0 {
		logprobs = string(e.Result.Logprobs)
	} // else leave nil -> SQL NULL

	// EVScore is *float64, nil when the EV path didn't fire (CLAUDE.md
	// §4.6) - pgx passes a nil pointer through as SQL NULL directly, which
	// is exactly the provenance signal ev_score is meant to carry.
	_, err = tx.Exec(ctx, `
		INSERT INTO scoring_events (call_id, composite_key, emitted_score, ev_score, reasoning, raw, logprobs)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, callID, compositeKey, e.Result.EmittedScore, e.Result.EVScore, e.Result.Reasoning, string(e.Result.Raw), logprobs)
	return wrapErrIfSet("insert scoring_events row", err)
}

func (s *azureStore) SeenJobs(ctx context.Context) ([]SeenJob, error) {
	rows, err := s.pool.Query(ctx, `SELECT stablekey, posted_at, last_seen FROM jobs`)
	if err != nil {
		return nil, wrapErr("query seen jobs", err)
	}
	defer rows.Close()

	var out []SeenJob
	for rows.Next() {
		var stablekey string
		var postedAt time.Time
		var lastSeen time.Time
		if err := rows.Scan(&stablekey, &postedAt, &lastSeen); err != nil {
			return nil, wrapErr("scan seen job row", err)
		}
		out = append(out, SeenJob{
			Stablekey:  stablekey,
			PostedAt:   postedAt.Unix(),
			HasApplied: false,
			LastSeen:   lastSeen,
		})
	}
	return out, wrapErrIfSet("iterate seen jobs", rows.Err())
}

// BumpLastSeen is a single batched UPDATE rather than AWS's per-item loop -
// Postgres supports set-based updates efficiently and this only ever touches
// the liveness bookkeeping row, never scoring_events, so batching carries
// none of RecordScores' fault-isolation concerns.
func (s *azureStore) BumpLastSeen(ctx context.Context, items []SeenJob, now time.Time) error {
	if len(items) == 0 {
		return nil
	}
	keys := make([]string, len(items))
	for i, it := range items {
		keys[i] = it.compositeKey()
	}
	_, err := s.pool.Exec(ctx, `UPDATE jobs SET last_seen = $1 WHERE composite_key = ANY($2)`, now, keys)
	return wrapErrIfSet("bump last_seen", err)
}

// DeleteAged is an intentional no-op on Azure: every jobs row that exists
// has already been scored (RecordScores always upserts jobs alongside
// inserting scoring_events), so a literal DELETE would either violate the
// scoring_events FK or (with ON DELETE CASCADE) destroy irreplaceable
// scoring events - violating "preserve every scoring event." AWS's
// DeleteAged exists for DynamoDB cache hygiene, which doesn't apply to an
// append-only measurement store; last_seen remains available for ETL to see
// when a posting vanished.
func (s *azureStore) DeleteAged(ctx context.Context, items []SeenJob) (int, error) {
	if len(items) > 0 {
		s.logger.Info("skipping delete of aged jobs on azure store by design", "count", len(items))
	}
	return 0, nil
}

func (s *azureStore) ExportRows(ctx context.Context) ([]ExportRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (j.stablekey)
			j.stablekey, j.posted_at, j.title, j.company, j.url,
			COALESCE(se.ev_score, se.emitted_score), COALESCE(se.reasoning, ''),
			COALESCE(j.payload->>'location', '')
		FROM jobs j
		JOIN scoring_events se ON se.composite_key = j.composite_key
		JOIN scoring_calls sc ON sc.call_id = se.call_id
		ORDER BY j.stablekey, sc.scored_at DESC
	`)
	if err != nil {
		return nil, wrapErr("query export rows", err)
	}
	defer rows.Close()

	var out []ExportRow
	for rows.Next() {
		var r ExportRow
		var postedAt time.Time
		if err := rows.Scan(&r.Stablekey, &postedAt, &r.Title, &r.Company, &r.URL,
			&r.Score, &r.Reasoning, &r.Location); err != nil {
			return nil, wrapErr("scan export row", err)
		}
		r.PostedAt = postedAt.Unix()
		r.HasApplied = false
		out = append(out, r)
	}
	return out, wrapErrIfSet("iterate export rows", rows.Err())
}

// BuildPanel inserts one panel_jobs row per selected job, all under a single
// new panel_id, in one transaction. panel_id encodes the seed and build
// timestamp (matching the table's own "one panel build (seed + built_at tag)"
// comment) purely as an opaque tag - MAX(built_at), not the string, is what
// ActivePanel's auto-detect actually sorts by.
func (s *azureStore) BuildPanel(ctx context.Context, seed int64, selection []PanelJob) (string, error) {
	builtAt := time.Now().UTC()
	panelID := fmt.Sprintf("panel-%d-%s", seed, builtAt.Format("20060102T150405Z"))

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", wrapErr("begin build panel tx", err)
	}
	defer tx.Rollback(ctx) // no-op after Commit

	for _, pj := range selection {
		payload, err := json.Marshal(pj.Job)
		if err != nil {
			return "", wrapErr("marshal panel job snapshot", err)
		}
		postedAt := time.Unix(pj.Job.PostedAt, 0)
		_, err = tx.Exec(ctx, `
			INSERT INTO panel_jobs (panel_id, stablekey, company, posted_at, score_band, screening_score, job_snapshot, seed, built_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`, panelID, pj.Job.createStableKey(), pj.Job.Company, postedAt, pj.Band, pj.ScreeningScore, string(payload), seed, builtAt)
		if err != nil {
			return "", wrapErr("insert panel_jobs row", err)
		}
	}

	return panelID, wrapErrIfSet("commit build panel tx", tx.Commit(ctx))
}

// ActivePanel returns the frozen job snapshots for panelID. An empty panelID
// auto-detects the most-recently-built panel (MAX(built_at)) - the mechanism
// that lets a normal panel rebuild become active without any env var/redeploy
// step. ok is false (with no error) when no matching panel exists yet.
func (s *azureStore) ActivePanel(ctx context.Context, panelID string) ([]Job, string, bool, error) {
	if panelID == "" {
		err := s.pool.QueryRow(ctx, `SELECT panel_id FROM panel_jobs ORDER BY built_at DESC LIMIT 1`).Scan(&panelID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, "", false, nil
			}
			return nil, "", false, wrapErr("resolve active panel_id", err)
		}
	}

	rows, err := s.pool.Query(ctx, `SELECT job_snapshot FROM panel_jobs WHERE panel_id = $1`, panelID)
	if err != nil {
		return nil, panelID, false, wrapErr("query panel_jobs snapshots", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, panelID, false, wrapErr("scan panel_jobs row", err)
		}
		var j Job
		if err := json.Unmarshal([]byte(raw), &j); err != nil {
			return nil, panelID, false, wrapErr("unmarshal job_snapshot", err)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, panelID, false, wrapErr("iterate panel_jobs rows", err)
	}
	return jobs, panelID, len(jobs) > 0, nil
}

func wrapErrIfSet(msg string, err error) error {
	if err == nil {
		return nil
	}
	return wrapErr(msg, err)
}
