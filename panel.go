package main

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
)

// screeningTemperature/screeningBatchSize govern the one-off screening pass
// that provisionally scores the full post-filter set for panel stratification
// (CLAUDE.md's fixed 30-job panel task). Temperature 0 keeps a same-day
// rebuild-without-reason reproducible, isolating PanelSeed as the only source
// of selection variance; batch size 10 minimizes HTTP calls over ~450 jobs.
const (
	screeningTemperature float32 = 0
	screeningBatchSize   int     = 2
)

// resolveScreeningModel looks up ScreeningModel()'s name against the real
// configured Models() list (not defaultAzureModels directly), so the
// screening pass automatically inherits real BaseURL/TPM/RPM once configured
// and works with local Ollama overrides via AZURE_MODELS.
func resolveScreeningModel(models []ModelConfig, name string) (ModelConfig, error) {
	for _, m := range models {
		if m.Name == name {
			return m, nil
		}
	}
	return ModelConfig{}, traceErrorf("screening model %q not found in configured models list", name)
}

// screenJobs runs model once over jobs to get a provisional score per job for
// stratification. It never writes to scoring_calls/scoring_events - CLAUDE.md
// is explicit that this score is audit-only (persisted as panel_jobs'
// screening_score column, a different table), never experimental data. A
// batch failure aborts the whole build (fail-fast) rather than silently
// banding on partial coverage.
func screenJobs(ctx context.Context, app *App, jobs []Job, model ModelConfig) (map[string]float64, error) {
	if model.TPM <= 0 || model.RPM <= 0 {
		return nil, traceErrorf("screening model %q missing TPM/RPM, refusing to screen", model.Name)
	}
	scorer := app.Scorer
	if app.Scorers != nil {
		s, ok := app.Scorers[model.Protocol]
		if !ok {
			return nil, traceErrorf("no scorer registered for screening model protocol %q", model.Protocol)
		}
		if model.BaseURL == "" {
			return nil, traceErrorf("screening model %q missing BaseURL", model.Name)
		}
		scorer = s
	}

	throttle, limiter := newModelThrottle(model)
	defer limiter.Stop()

	scores := make(map[string]float64, len(jobs))
	for i := 0; i < len(jobs); i += screeningBatchSize {
		<-limiter.C
		end := min(i+screeningBatchSize, len(jobs))

		tokenEstimate := 3000.0
		var descChars int
		for _, j := range jobs[i:end] {
			descChars += len(j.Description)
		}
		tokenEstimate += float64(descChars) / 1.75

		if err := throttle.reserve(ctx, tokenEstimate); err != nil {
			return nil, wrapErr("throttle reserve during screening", err)
		}
		results, usage, err := app.scoreBatchRetry(ctx, scorer, jobs[i:end], model, screeningTemperature)
		if err != nil {
			return nil, wrapErr(fmt.Sprintf("screening batch failed at %d", i), err)
		}
		throttle.record(usage.Total)
		for _, r := range results {
			scores[r.JobKey] = r.EmittedScore
		}
	}
	return scores, nil
}

// screenedJob pairs a Job with its provisional screening score.
type screenedJob struct {
	Job   Job
	Score float64
}

// sortBands returns bands sorted ascending by Min - the order bandFor
// expects.
func sortBands(bands []ScoreBand) []ScoreBand {
	out := make([]ScoreBand, len(bands))
	copy(out, bands)
	sort.Slice(out, func(i, j int) bool { return out[i].Min < out[j].Min })
	return out
}

// bandFor returns the ScoreBand score falls into, given bands already sorted
// ascending by Min. Half-open [Min,Max) except the band with the greatest
// Min, which is Max-inclusive so a perfect top score isn't dropped.
func bandFor(score float64, bandsAscByMin []ScoreBand) (ScoreBand, bool) {
	for i, b := range bandsAscByMin {
		isTop := i == len(bandsAscByMin)-1
		if score >= b.Min && (score < b.Max || (isTop && score <= b.Max)) {
			return b, true
		}
	}
	return ScoreBand{}, false
}

// bucketByBand groups jobs into their score band. Jobs are deduped by
// stablekey first (keeping the job with the greatest PostedAt on a
// collision), so a reposted listing appearing twice in jobs can never
// produce two rows sharing panel_jobs' PK (panel_id, stablekey). A job with
// no screening score (the model silently dropped its JobKey from a batch
// response) is logged and excluded, never treated as score 0. Winners are
// walked in stablekey-sorted order - a canonical baseline established before
// any randomness touches this data, so Go's randomized map iteration order
// never influences selection, only PanelSeed does.
func bucketByBand(jobs []Job, scores map[string]float64, bands []ScoreBand) (map[string][]screenedJob, error) {
	bandsAsc := sortBands(bands)

	winners := make(map[string]Job, len(jobs))
	for _, j := range jobs {
		key := j.createStableKey()
		if existing, ok := winners[key]; !ok || j.PostedAt > existing.PostedAt {
			winners[key] = j
		}
	}

	stablekeys := make([]string, 0, len(winners))
	for k := range winners {
		stablekeys = append(stablekeys, k)
	}
	sort.Strings(stablekeys)

	buckets := make(map[string][]screenedJob)
	for _, sk := range stablekeys {
		j := winners[sk]
		score, ok := scores[j.Key]
		if !ok {
			app.Logger.Warn("no screening score for job, excluding from panel selection", "key", j.Key, "stablekey", sk)
			continue
		}
		band, ok := bandFor(score, bandsAsc)
		if !ok {
			return nil, traceErrorf("screening score %v for job %q falls outside all configured score bands", score, sk)
		}
		buckets[band.Name] = append(buckets[band.Name], screenedJob{Job: j, Score: score})
	}
	return buckets, nil
}

// remainingPool filters out jobs already selected (by stablekey).
func remainingPool(all []screenedJob, selected map[string]bool) []screenedJob {
	out := make([]screenedJob, 0, len(all))
	for _, sj := range all {
		if !selected[sj.Job.createStableKey()] {
			out = append(out, sj)
		}
	}
	return out
}

// roundRobinTake seeded-shuffles company order and each company's internal
// job order (after first sorting both into a canonical baseline - companies
// alphabetically, jobs by stablekey - so the shuffle is the only source of
// variation), then takes up to n jobs company-by-company in rotation: a
// company's 2nd pick only happens once every other company still in
// rotation has had its 1st. Companies at/over maxPerCompany (when >0, using
// the running total across the whole panel) are skipped. selected/
// companyCounts are updated in place so a job already taken in an earlier
// phase/band is never re-picked and the cap holds across bands.
func roundRobinTake(pool []screenedJob, n int, companyCounts map[string]int, maxPerCompany int, selected map[string]bool, rng *rand.Rand) []screenedJob {
	if n <= 0 || len(pool) == 0 {
		return nil
	}

	byCompany := make(map[string][]screenedJob)
	for _, sj := range pool {
		byCompany[sj.Job.Company] = append(byCompany[sj.Job.Company], sj)
	}
	companies := make([]string, 0, len(byCompany))
	for c := range byCompany {
		companies = append(companies, c)
	}
	sort.Strings(companies)
	for _, c := range companies {
		jobs := byCompany[c]
		sort.Slice(jobs, func(i, j int) bool {
			return jobs[i].Job.createStableKey() < jobs[j].Job.createStableKey()
		})
		byCompany[c] = jobs
	}

	rng.Shuffle(len(companies), func(i, j int) { companies[i], companies[j] = companies[j], companies[i] })
	for _, c := range companies {
		jobs := byCompany[c]
		rng.Shuffle(len(jobs), func(i, j int) { jobs[i], jobs[j] = jobs[j], jobs[i] })
	}

	var out []screenedJob
	for len(out) < n {
		progressed := false
		for _, c := range companies {
			if len(out) >= n {
				break
			}
			if maxPerCompany > 0 && companyCounts[c] >= maxPerCompany {
				continue
			}
			jobs := byCompany[c]
			if len(jobs) == 0 {
				continue
			}
			sj := jobs[0]
			byCompany[c] = jobs[1:]
			key := sj.Job.createStableKey()
			if selected[key] {
				continue
			}
			selected[key] = true
			companyCounts[c]++
			out = append(out, sj)
			progressed = true
		}
		if !progressed {
			break // no company has jobs left, or every remaining company is capped
		}
	}
	return out
}

// selectPanel deterministically picks panelSize jobs from buckets given
// per-band targets/floors, a global per-company cap, and a seed. Same
// buckets+bands+targets+maxPerCompany+seed always yields the same set of
// stablekeys, regardless of upstream slice/map ordering.
//
// Three seeded phases, each processing bands in descending-Min order (top
// band first), consuming the same rng in that fixed sequence so the whole
// build is one deterministic draw from seed:
//  1. Floors: for each band with Floor>0, round-robin-take up to Floor jobs.
//  2. Targets: for each band, round-robin-take up to (Target - already-taken)
//     more from that band's remaining pool.
//  3. Backfill: if total selected < panelSize (some band came up short of
//     distinct/uncapped jobs), keep round-robin-taking from any band with
//     leftover jobs, top band first, until panelSize is reached or no jobs
//     remain anywhere.
func selectPanel(buckets map[string][]screenedJob, bands []ScoreBand, targets []BandTarget, panelSize, maxPerCompany int, seed int64) ([]screenedJob, error) {
	bandsDesc := sortBands(bands)
	for i, j := 0, len(bandsDesc)-1; i < j; i, j = i+1, j-1 {
		bandsDesc[i], bandsDesc[j] = bandsDesc[j], bandsDesc[i]
	}

	targetByBand := make(map[string]BandTarget, len(targets))
	for _, t := range targets {
		targetByBand[t.Band] = t
	}

	rng := rand.New(rand.NewSource(seed))
	selected := make(map[string]bool)
	companyCounts := make(map[string]int)
	bandTaken := make(map[string]int)
	var result []screenedJob

	take := func(bandName string, n int) {
		if n <= 0 {
			return
		}
		pool := remainingPool(buckets[bandName], selected)
		taken := roundRobinTake(pool, n, companyCounts, maxPerCompany, selected, rng)
		result = append(result, taken...)
		bandTaken[bandName] += len(taken)
	}

	// Phase 1: floors, top band first.
	for _, b := range bandsDesc {
		if t, ok := targetByBand[b.Name]; ok && t.Floor > 0 {
			take(b.Name, t.Floor)
		}
	}

	// Phase 2: targets (remaining slots up to Target), top band first.
	for _, b := range bandsDesc {
		if t, ok := targetByBand[b.Name]; ok {
			take(b.Name, t.Target-bandTaken[b.Name])
		}
	}

	// Phase 3: backfill from any band with leftovers, top band first.
	for _, b := range bandsDesc {
		if len(result) >= panelSize {
			break
		}
		take(b.Name, panelSize-len(result))
	}

	if len(result) > panelSize {
		result = result[:panelSize]
	}
	return result, nil
}

// loadOrBuildPanel resolves the active panel (auto-detected via MAX(built_at)
// unless ActivePanelID pins a specific one) or builds a fresh one if none
// exists yet or RebuildPanel is set.
func loadOrBuildPanel(ctx context.Context, app *App, jobs []Job) ([]Job, error) {
	pinned := app.Config.ActivePanelID()
	if !app.Config.RebuildPanel() {
		loaded, resolvedID, ok, err := app.Store.ActivePanel(ctx, pinned)
		if err != nil {
			return nil, err
		}
		if ok {
			app.Logger.Info("using existing panel", "panel_id", resolvedID)
			return loaded, nil
		}
		if pinned != "" {
			return nil, traceErrorf("AZURE_ACTIVE_PANEL_ID %q pins a panel that does not exist", pinned)
		}
		app.Logger.Info("no active panel found, building one")
	} else {
		app.Logger.Info("AZURE_REBUILD_PANEL set, forcing a fresh panel build")
	}
	return buildPanel(ctx, app, jobs)
}

// buildPanel runs the screening pass, selects the panel, persists it, and
// returns the frozen job snapshots to score in this run.
func buildPanel(ctx context.Context, app *App, jobs []Job) ([]Job, error) {
	seed := app.Config.PanelSeed()
	if seed == 0 {
		return nil, traceErrorf("missing panel seed - set AZURE_PANEL_SEED (required, recorded with the panel for reproducibility)")
	}

	models, err := app.Config.Models(ctx)
	if err != nil {
		return nil, wrapErr("loading models for screening lookup", err)
	}
	model, err := resolveScreeningModel(models, app.Config.ScreeningModel())
	if err != nil {
		return nil, err
	}

	scores, err := screenJobs(ctx, app, jobs, model)
	if err != nil {
		return nil, wrapErr("screening pass failed", err)
	}

	bands, err := app.Config.ScoreBands()
	if err != nil {
		return nil, err
	}
	targets, err := app.Config.BandTargets()
	if err != nil {
		return nil, err
	}

	buckets, err := bucketByBand(jobs, scores, bands)
	if err != nil {
		return nil, err
	}

	panelSize := app.Config.PanelSize()
	selection, err := selectPanel(buckets, bands, targets, panelSize, app.Config.MaxPerCompany(), seed)
	if err != nil {
		return nil, err
	}
	if len(selection) < panelSize {
		app.Logger.Warn("panel build could not fill every slot", "want", panelSize, "got", len(selection))
	}

	bandsAsc := sortBands(bands)
	rows := make([]PanelJob, len(selection))
	for i, sj := range selection {
		band, _ := bandFor(sj.Score, bandsAsc)
		rows[i] = PanelJob{Job: sj.Job, Band: band.Name, ScreeningScore: sj.Score}
	}

	panelID, err := app.Store.BuildPanel(ctx, seed, rows)
	if err != nil {
		return nil, wrapErr("persisting panel", err)
	}
	app.Logger.Info("built new panel", "panel_id", panelID, "size", len(rows))

	out := make([]Job, len(rows))
	for i, r := range rows {
		out[i] = r.Job
	}
	return out, nil
}
