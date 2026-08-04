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

// malformedKeyEmittedScore is the sentinel emitted_score persisted for a
// scoring_events row that could not be correlated to any job in its batch.
// -2 is outside the valid 0-100 rubric range and distinct from a legitimate
// score of 0, so it can never be confused with real model output.
//
// VERSIONED CONSTANT (CLAUDE.md §4.8, v1) - changing this value is a
// breaking change for anyone querying on it; introduce a new
// value/version rather than editing this one in place.
const malformedKeyEmittedScore float64 = -2

// zipScoreEvents joins jobs with their scoring results by JobKey. A job with
// a matching result produces a normal event. A job with no matching result
// is either (a) paired best-effort, by position, with a "leftover" result
// that itself couldn't be claimed by any job's key (an item whose JSON
// failed to parse, or whose key didn't match anything in the batch) - this
// produces a sentinel event (malformedKeyEmittedScore, CLAUDE.md §4.8)
// rather than losing the raw payload - or (b) if no leftover result remains
// to pair with, logged and dropped: the provider genuinely returned nothing
// for that job. batchIndex is stamped onto every event so the run's
// persistence log (store_azure.go) can be correlated back to the
// scoring-time log for the same batch.
func zipScoreEvents(a *App, jobs []Job, results []ScoreResult, batchIndex int) []ScoringEvent {
	claimed := make([]bool, len(results))
	byKey := make(map[string]int, len(results))
	for i, r := range results {
		if r.JobKey == "" {
			continue // never a real match target - see scorer_openai.go sentinel JobKey
		}
		if _, exists := byKey[r.JobKey]; !exists {
			byKey[r.JobKey] = i
		}
	}

	events := make([]ScoringEvent, 0, len(jobs))
	var unmatchedJobs []Job
	for _, j := range jobs {
		i, ok := byKey[j.Key]
		if !ok {
			unmatchedJobs = append(unmatchedJobs, j)
			continue
		}
		claimed[i] = true
		events = append(events, ScoringEvent{Job: j, Result: results[i], BatchIndex: batchIndex})
	}

	var leftovers []ScoreResult
	for i, r := range results {
		if !claimed[i] {
			leftovers = append(leftovers, r)
		}
	}

	// Best-effort positional pairing (CLAUDE.md §4.8): once key-matching has
	// already failed, order within the batch is the only signal left. This
	// can mis-attribute which payload lands on which job if the provider
	// didn't preserve order for its failed items - accepted deliberately,
	// same trade-off as bestEffortScoreEV's "best-effort, never required".
	n := len(unmatchedJobs)
	if len(leftovers) < n {
		n = len(leftovers)
	}
	for i := 0; i < n; i++ {
		job := unmatchedJobs[i]
		r := leftovers[i]
		r.EmittedScore = malformedKeyEmittedScore // authoritative override regardless of what the leftover carried
		r.EVScore = nil                           // always NULL on a sentinel row (CLAUDE.md §4.8)
		a.Logger.Warn("scoring result could not be correlated to a job key; persisting sentinel row",
			"key", job.Key, "batch_index", batchIndex, "model", r.Model)
		events = append(events, ScoringEvent{Job: job, Result: r, BatchIndex: batchIndex})
	}
	// Genuinely missing from the response - no leftover to pair with. Unchanged behavior.
	for i := n; i < len(unmatchedJobs); i++ {
		a.Logger.Warn("no score returned for job", "key", unmatchedJobs[i].Key)
	}

	return events
}
