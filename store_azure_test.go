package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func newTestAzureStore(t *testing.T) *azureStore {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set; skipping (requires docker compose up -d)")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connecting to test postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	return newAzureStore(slog.New(slog.NewTextHandler(os.Stderr, nil)), pool)
}

func TestAzureStoreRecordScoresPreservesEveryEvent(t *testing.T) {
	s := newTestAzureStore(t)
	ctx := context.Background()

	job := Job{Company: "Acme", Title: "SWE", Location: "Remote", Source: "greenhouse", PostedAt: time.Now().Unix()}
	events := []ScoringEvent{
		{Job: job, Result: ScoreResult{Model: "gpt-4.1-mini", EmittedScore: 71.2, Reasoning: "r1", Raw: json.RawMessage(`{"a":1}`), ScoredAt: time.Now()}},
		{Job: job, Result: ScoreResult{Model: "phi-4", EmittedScore: 65.5, Reasoning: "r2", Raw: json.RawMessage(`{"a":2}`), ScoredAt: time.Now().Add(time.Minute)}},
	}
	if err := s.RecordScores(ctx, events, "test-contributor", "test-resume", "test-config", "test-instructions-v1"); err != nil {
		t.Fatalf("RecordScores: %v", err)
	}

	// Two events, same stablekey, two different models -> must be two rows,
	// never collapsed - assert via ExportRows picking the most recent.
	rows, err := s.ExportRows(ctx)
	if err != nil {
		t.Fatalf("ExportRows: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Stablekey == job.createStableKey() {
			found = true
			if r.Reasoning != "r2" {
				t.Fatalf("expected most-recent event (phi-4/r2), got reasoning=%q", r.Reasoning)
			}
		}
	}
	if !found {
		t.Fatal("expected exported row for test job")
	}
}

// TestAzureStoreRecordScoresKeepsEmittedAndEVScoreSeparate is the regression
// test for CLAUDE.md §4.6: emitted_score and ev_score must land in two
// distinct columns, never collapsed into one, and NULL ev_score must mean
// "EV path didn't fire" (row 2 below), not "EV path fired and produced
// NULL." Queries the columns directly rather than via ExportRows, since
// ExportRows' COALESCE(ev_score, emitted_score) is a display convenience
// that would mask exactly the bug this test exists to catch.
func TestAzureStoreRecordScoresKeepsEmittedAndEVScoreSeparate(t *testing.T) {
	s := newTestAzureStore(t)
	ctx := context.Background()

	ev := 68.4
	jobWithEV := Job{Company: "Gamma", Title: "Data Eng", Location: "Remote", Source: "greenhouse", PostedAt: time.Now().Unix()}
	jobNoEV := Job{Company: "Delta", Title: "Data Eng", Location: "Remote", Source: "greenhouse", PostedAt: time.Now().Unix() + 1}
	events := []ScoringEvent{
		{Job: jobWithEV, Result: ScoreResult{Model: "m-with-ev", EmittedScore: 70, EVScore: &ev, Raw: json.RawMessage(`{}`), ScoredAt: time.Now()}},
		{Job: jobNoEV, Result: ScoreResult{Model: "m-no-ev", EmittedScore: 42, EVScore: nil, Raw: json.RawMessage(`{}`), ScoredAt: time.Now()}},
	}
	if err := s.RecordScores(ctx, events, "test-contributor", "test-resume", "test-config", "test-instructions-v1"); err != nil {
		t.Fatalf("RecordScores: %v", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT j.company, se.emitted_score, se.ev_score
		FROM scoring_events se JOIN jobs j ON j.composite_key = se.composite_key
		WHERE j.company IN ('Gamma', 'Delta')
	`)
	if err != nil {
		t.Fatalf("query scoring_events: %v", err)
	}
	defer rows.Close()

	found := map[string]bool{}
	for rows.Next() {
		var company string
		var emitted float64
		var evScore *float64
		if err := rows.Scan(&company, &emitted, &evScore); err != nil {
			t.Fatalf("scan: %v", err)
		}
		found[company] = true
		switch company {
		case "Gamma":
			if emitted != 70 {
				t.Errorf("Gamma emitted_score = %v, want 70", emitted)
			}
			if evScore == nil || *evScore != 68.4 {
				t.Errorf("Gamma ev_score = %v, want 68.4", evScore)
			}
		case "Delta":
			if emitted != 42 {
				t.Errorf("Delta emitted_score = %v, want 42", emitted)
			}
			if evScore != nil {
				t.Errorf("Delta ev_score = %v, want NULL (EV path didn't fire)", *evScore)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rows: %v", err)
	}
	if !found["Gamma"] || !found["Delta"] {
		t.Fatalf("expected rows for both Gamma and Delta, got %v", found)
	}
}

// TestAzureStoreRecordScoresPersistsItemizedTokens is the regression test
// for CLAUDE.md §4.2: scoring_calls' itemized token columns (and deployment)
// must actually be populated from ScoreResult.Usage now, not left NULL by
// design (see the old comment this fix removed in store_azure.go). It
// covers both a fully-itemized call and a call whose provider only reported
// a bare total - the itemized columns must be NULL there specifically
// because the provider didn't itemize, not because the plumbing is missing,
// while usage_raw still round-trips either way as the backstop.
func TestAzureStoreRecordScoresPersistsItemizedTokens(t *testing.T) {
	s := newTestAzureStore(t)
	ctx := context.Background()

	inputUncached, cacheRead, output, reasoning := int64(60), int64(40), int64(30), int64(20)
	itemizedUsage := Usage{
		InputUncached: &inputUncached,
		CacheRead:     &cacheRead,
		Output:        &output,
		Reasoning:     &reasoning,
		Total:         150,
		Raw:           json.RawMessage(`{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}`),
	}
	bareUsage := Usage{
		Total: 75,
		Raw:   json.RawMessage(`{"total_tokens":75}`),
	}

	jobItemized := Job{Company: "Epsilon", Title: "MLE", Location: "Remote", Source: "greenhouse", PostedAt: time.Now().Unix()}
	jobBare := Job{Company: "Zeta", Title: "MLE", Location: "Remote", Source: "greenhouse", PostedAt: time.Now().Unix() + 1}
	events := []ScoringEvent{
		{Job: jobItemized, Result: ScoreResult{Model: "m-itemized", Deployment: "dep-1", EmittedScore: 80, Usage: itemizedUsage, Raw: json.RawMessage(`{}`), ScoredAt: time.Now()}},
		{Job: jobBare, Result: ScoreResult{Model: "m-bare", Deployment: "dep-2", EmittedScore: 55, Usage: bareUsage, Raw: json.RawMessage(`{}`), ScoredAt: time.Now()}},
	}
	if err := s.RecordScores(ctx, events, "test-contributor", "test-resume", "test-config", "test-instructions-v1"); err != nil {
		t.Fatalf("RecordScores: %v", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT model, deployment, input_uncached, cached_read, cache_write, output_tokens, reasoning_tokens, usage_raw
		FROM scoring_calls
		WHERE model IN ('m-itemized', 'm-bare')
	`)
	if err != nil {
		t.Fatalf("query scoring_calls: %v", err)
	}
	defer rows.Close()

	found := map[string]bool{}
	for rows.Next() {
		var model, deployment string
		var inputUncached, cacheRead, cacheWrite, output, reasoning *int64
		var usageRaw []byte
		if err := rows.Scan(&model, &deployment, &inputUncached, &cacheRead, &cacheWrite, &output, &reasoning, &usageRaw); err != nil {
			t.Fatalf("scan: %v", err)
		}
		found[model] = true
		switch model {
		case "m-itemized":
			if deployment != "dep-1" {
				t.Errorf("deployment = %q, want dep-1", deployment)
			}
			if inputUncached == nil || *inputUncached != 60 {
				t.Errorf("input_uncached = %v, want 60", inputUncached)
			}
			if cacheRead == nil || *cacheRead != 40 {
				t.Errorf("cached_read = %v, want 40", cacheRead)
			}
			if cacheWrite != nil {
				t.Errorf("cache_write = %v, want NULL (no OpenAI-compatible provider reports this)", *cacheWrite)
			}
			if output == nil || *output != 30 {
				t.Errorf("output_tokens = %v, want 30", output)
			}
			if reasoning == nil || *reasoning != 20 {
				t.Errorf("reasoning_tokens = %v, want 20", reasoning)
			}
			if len(usageRaw) == 0 {
				t.Error("usage_raw is empty, want the verbatim usage blob")
			}
		case "m-bare":
			if inputUncached != nil || cacheRead != nil || cacheWrite != nil || output != nil || reasoning != nil {
				t.Errorf("expected all itemized fields NULL for a provider that didn't itemize, got input_uncached=%v cached_read=%v cache_write=%v output_tokens=%v reasoning_tokens=%v",
					inputUncached, cacheRead, cacheWrite, output, reasoning)
			}
			if len(usageRaw) == 0 {
				t.Error("usage_raw is empty, want the verbatim usage blob even without itemization")
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rows: %v", err)
	}
	if !found["m-itemized"] || !found["m-bare"] {
		t.Fatalf("expected rows for both m-itemized and m-bare, got %v", found)
	}
}

func TestAzureStoreSeenJobsBumpAndDeleteAged(t *testing.T) {
	s := newTestAzureStore(t)
	ctx := context.Background()

	job := Job{Company: "Beta", Title: "PM", Location: "NYC", Source: "lever", PostedAt: time.Now().Unix()}
	events := []ScoringEvent{{Job: job, Result: ScoreResult{Model: "m1", EmittedScore: 50, Raw: json.RawMessage(`{}`), ScoredAt: time.Now()}}}
	if err := s.RecordScores(ctx, events, "test-contributor", "test-resume", "test-config", "test-instructions-v1"); err != nil {
		t.Fatalf("RecordScores: %v", err)
	}

	seen, err := s.SeenJobs(ctx)
	if err != nil {
		t.Fatalf("SeenJobs: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("expected at least one seen job")
	}

	now := time.Now()
	if err := s.BumpLastSeen(ctx, seen, now); err != nil {
		t.Fatalf("BumpLastSeen: %v", err)
	}

	n, err := s.DeleteAged(ctx, seen)
	if err != nil {
		t.Fatalf("DeleteAged: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected DeleteAged to be a no-op on azure store, got %d deleted", n)
	}
}

// TestAzureStoreBuildPanelAndActivePanelRoundtrip covers the fixed
// score-stratified 30-job panel task's persistence layer: a build's job
// snapshots must round-trip verbatim, and auto-detect (empty panelID) must
// always resolve to the most-recently-built panel while an explicit,
// older panel_id still resolves correctly (CLAUDE.md's "no env var/redeploy
// needed after a normal build" decision).
func TestAzureStoreBuildPanelAndActivePanelRoundtrip(t *testing.T) {
	s := newTestAzureStore(t)
	ctx := context.Background()

	job1 := Job{Key: "k1", Company: "Acme", Title: "SWE", Location: "Remote", Source: "greenhouse", PostedAt: time.Now().Unix()}
	job2 := Job{Key: "k2", Company: "Beta", Title: "MLE", Location: "Remote", Source: "ashby", PostedAt: time.Now().Unix() + 1}

	firstID, err := s.BuildPanel(ctx, 111, []PanelJob{
		{Job: job1, Band: "viable", ScreeningScore: 85},
		{Job: job2, Band: "borderline", ScreeningScore: 65},
	})
	if err != nil {
		t.Fatalf("BuildPanel (first): %v", err)
	}

	loaded, resolvedID, ok, err := s.ActivePanel(ctx, "")
	if err != nil {
		t.Fatalf("ActivePanel(\"\") after first build: %v", err)
	}
	if !ok {
		t.Fatal("ActivePanel(\"\") returned ok=false right after a build")
	}
	if resolvedID != firstID {
		t.Fatalf("ActivePanel(\"\") resolved %q, want the just-built panel %q", resolvedID, firstID)
	}
	if len(loaded) != 2 {
		t.Fatalf("ActivePanel(\"\") returned %d jobs, want 2", len(loaded))
	}
	byKey := map[string]Job{}
	for _, j := range loaded {
		byKey[j.Key] = j
	}
	if byKey["k1"].Company != "Acme" || byKey["k2"].Company != "Beta" {
		t.Fatalf("loaded snapshots don't match what was built: %+v", byKey)
	}

	time.Sleep(10 * time.Millisecond) // ensure built_at strictly increases for the ordering assertion below
	secondID, err := s.BuildPanel(ctx, 222, []PanelJob{
		{Job: job1, Band: "viable", ScreeningScore: 90},
	})
	if err != nil {
		t.Fatalf("BuildPanel (second): %v", err)
	}
	if secondID == firstID {
		t.Fatalf("second build produced the same panel_id as the first: %q", secondID)
	}

	_, resolvedID, ok, err = s.ActivePanel(ctx, "")
	if err != nil {
		t.Fatalf("ActivePanel(\"\") after second build: %v", err)
	}
	if !ok || resolvedID != secondID {
		t.Fatalf("ActivePanel(\"\") after second build resolved (%q, %v), want the newer panel %q", resolvedID, ok, secondID)
	}

	pinned, resolvedID, ok, err := s.ActivePanel(ctx, firstID)
	if err != nil {
		t.Fatalf("ActivePanel(firstID) pinned: %v", err)
	}
	if !ok || resolvedID != firstID || len(pinned) != 2 {
		t.Fatalf("pinning the older panel_id %q should still resolve it, got resolvedID=%q ok=%v jobs=%d", firstID, resolvedID, ok, len(pinned))
	}
}
