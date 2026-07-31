package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"
)

// throttleDerate protects a margin below each model's real TPM/RPM quota
// (CLAUDE.md §8).
const throttleDerate = 0.75

// tokenBudget is the per-window token ceiling for a model's declared TPM.
func tokenBudget(tpm int) float64 {
	return math.Floor(throttleDerate * float64(tpm))
}

// requestInterval is the minimum spacing between requests for a model's
// declared RPM. Ceil, not floor: flooring would round the interval down,
// pushing the real request rate faster - toward the limit, not away from it.
func requestInterval(rpm int) time.Duration {
	seconds := math.Ceil(60.0 / (throttleDerate * float64(rpm)))
	return time.Duration(seconds) * time.Second
}

// newModelThrottle builds the per-model throttle for one run (CLAUDE.md §8):
// a token budget and a request-interval ticker, both derated. Callers must
// only pass a model with TPM>0 and RPM>0 - refusing to score an
// unconfigured model is the caller's job (main.go's handler loop), not this
// constructor's.
func newModelThrottle(model ModelConfig) (*tpmThrottle, *time.Ticker) {
	throttle := &tpmThrottle{
		budget: tokenBudget(model.TPM),
		window: 60 * time.Second,
	}
	return throttle, time.NewTicker(requestInterval(model.RPM))
}

type tpmThrottle struct {
	budget  float64
	window  time.Duration
	history []sample
}

type sample struct {
	at     time.Time
	tokens float64
}

type statusError struct{ code int }

func (e *statusError) Error() string {
	return fmt.Sprintf("unexpected status code: %d", e.code)
}

func (t *tpmThrottle) reserve(ctx context.Context, est float64) error {
	for {
		deadline := time.Now().Add(-t.window)
		sum, keep := 0.0, t.history[:0]
		for _, s := range t.history {
			if s.at.After(deadline) {
				keep = append(keep, s)
				sum += s.tokens
			}
		}
		t.history = keep
		if sum+est <= t.budget {
			return nil
		}
		if len(t.history) == 0 {
			app.Logger.Warn("batch estimate exceeds full budget", "est", est)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Until(t.history[0].at.Add(t.window))):
			continue
		}
	}
}

func (t *tpmThrottle) record(tokens float64) {
	t.history = append(t.history, sample{
		at:     time.Now(),
		tokens: tokens,
	})
}

func classify(err error) bool {
	var se *statusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.code {
	case 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

func (a *App) scoreBatchRetry(ctx context.Context, scorer Scorer, batch []Job, model ModelConfig, temperature float32) ([]ScoreResult, Usage, error) {
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		res, usage, err := scorer.ScoreBatch(ctx, batch, model, temperature)
		if err == nil {
			return res, usage, nil
		}
		if !classify(err) {
			return nil, Usage{}, err // fail fast
		}
		wait := time.Second << attempt // 1,2,4,8,16s
		if wait > 60*time.Second {
			wait = 60 * time.Second
		}
		wait += time.Duration(rand.Int63n(int64(time.Second)))
		a.Logger.Warn("retrying batch", "attempt", attempt+1, "wait", wait, "err", err)
		select {
		case <-ctx.Done():
			return nil, Usage{}, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, Usage{}, traceErrorf("batch failed after %d attempts", maxAttempts)
}
