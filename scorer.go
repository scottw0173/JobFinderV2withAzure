package main

import (
	"context"
	"encoding/json"
	"time"
)

// Scorer scores a batch of jobs against one model in a single logical call -
// this matches the real Gemini code (one HTTP call scores up to 5 jobs,
// sharing a token-throttle budget), not a one-job-at-a-time shape. Results
// correlate back to input jobs via ScoreResult.JobKey rather than by
// position, since a provider may drop or reorder a job in its response.
// The returned Usage is the call-level token facts (CLAUDE.md §4.2); its
// zero value (Total 0, all itemized fields nil) is the correct return when
// the provider doesn't report usage at all.
type Scorer interface {
	ScoreBatch(ctx context.Context, jobs []Job, model ModelConfig, temperature float32) ([]ScoreResult, Usage, error)
}

// Usage is one call's itemized token facts (CLAUDE.md §4.2). Token counts
// are reported once, in the response, and aren't reconstructable later, so
// this captures as finely as the provider itemizes. Each itemized field is
// *int64 and nil specifically means "the provider's response didn't itemize
// this" - itself information, never to be read as an asserted zero. Total
// and Raw are the two facts every provider that reports usage at all
// supplies; the rest degrade independently per provider.
type Usage struct {
	InputUncached *int64          // prompt tokens not served from cache
	CacheRead     *int64          // prompt tokens served from cache
	CacheWrite    *int64          // tokens written to cache (Anthropic-style; no protocol scorer built yet exposes this)
	Output        *int64          // visible completion tokens
	Reasoning     *int64          // hidden CoT tokens
	Total         float64         // total tokens for this call; drives throttle bookkeeping regardless of itemization
	Raw           json.RawMessage // verbatim usage blob, backstop
}

// ScoreResult keeps EmittedScore and EVScore as two separate facts, never
// collapsed into one (CLAUDE.md §4.6): EmittedScore is always populated - the
// number the model actually returned. EVScore is the probability-weighted
// expected value over the score token, and is nil whenever logprobs were
// absent/unusable and the EV path didn't fire - that nil is itself the
// provenance signal marking which rows the EV path didn't reach. A single
// blended field would silently mix two different kinds of measurement in a
// way that correlates with model, and erase per row which one you got.
type ScoreResult struct {
	JobKey       string
	Model        string
	Deployment   string   // model.Deployment, duplicated per-item from the batch call - same pattern as Temperature/Usage below
	EmittedScore float64  // raw fact: the number the model actually returned. Always populated.
	EVScore      *float64 // derived fact: logprob EV. Nil where logprobs absent/unusable/multi-token.
	Usage        Usage    // call-level token facts (CLAUDE.md §4.2), duplicated per-item - same pattern as Temperature below
	Reasoning    string
	SubScores    map[string]float64 // rubric dimensions; empty until the rubric lands
	Raw          json.RawMessage    // full structured output, stored verbatim
	Logprobs     json.RawMessage    // score-token distribution; nil where unsupported
	ScoredAt     time.Time
	Temperature  float64 // run-level value actually sent (CLAUDE.md §4.7); 0 where unused (AWS path)
}

// zipScoreEvents joins jobs with their scoring results by JobKey. A job with
// no matching result (the provider dropped it) is logged and skipped rather
// than producing a zero-value event.
func zipScoreEvents(a *App, jobs []Job, results []ScoreResult) []ScoringEvent {
	byKey := make(map[string]ScoreResult, len(results))
	for _, r := range results {
		byKey[r.JobKey] = r
	}
	events := make([]ScoringEvent, 0, len(jobs))
	for _, j := range jobs {
		r, ok := byKey[j.Key]
		if !ok {
			a.Logger.Warn("no score returned for job", "key", j.Key)
			continue
		}
		events = append(events, ScoringEvent{Job: j, Result: r})
	}
	return events
}
