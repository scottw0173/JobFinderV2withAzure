package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"os"
	"testing"
	"time"
)

// testInstructions is deliberately short (unlike the real config/instructions.md,
// which holds personal candidate data and isn't guaranteed to exist in a clean
// checkout) - this test verifies the scorer's wire protocol and JobKey
// correlation, not rubric quality.
const testInstructions = `You are scoring jobs for fit. For each job, return its
"key" value unchanged, a score, and a one-sentence reasoning.`

func newTestOpenAIScorer(t *testing.T) (*openaiScorer, string) {
	t.Helper()
	baseURL := os.Getenv("AZURE_OPENAI_TEST_ENDPOINT")
	if baseURL == "" {
		t.Skip("AZURE_OPENAI_TEST_ENDPOINT not set; skipping (requires docker compose up -d ollama + a pulled model)")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := &http.Client{Timeout: 3 * time.Minute}
	return newOpenAIScorer(logger, client, "", nil, []byte(testInstructions)), baseURL
}

func TestOpenAIScorerScoreBatchCorrelatesByKey(t *testing.T) {
	s, baseURL := newTestOpenAIScorer(t)
	ctx := context.Background()

	model := ModelConfig{Name: os.Getenv("AZURE_OPENAI_TEST_MODEL"), BaseURL: baseURL, Protocol: "openai"}
	if model.Name == "" {
		model.Name = "phi4-mini"
	}

	jobs := []Job{
		{Key: "acmeSWERemote1000", Title: "Software Engineer", Company: "Acme", Location: "Remote", Description: "Build backend services in Go."},
		{Key: "betaPMNYC2000", Title: "Product Manager", Company: "Beta", Location: "NYC", Description: "Own the roadmap for a small B2B product."},
	}

	results, usage, err := s.ScoreBatch(ctx, jobs, model, 0.7)
	if err != nil {
		t.Fatalf("ScoreBatch: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if usage.Total <= 0 {
		t.Errorf("expected nonzero token usage, got %v", usage.Total)
	}

	validKeys := map[string]bool{"acmeSWERemote1000": true, "betaPMNYC2000": true}
	for _, r := range results {
		if !validKeys[r.JobKey] {
			t.Errorf("result JobKey %q does not match any input job key", r.JobKey)
		}
		if len(r.Raw) == 0 {
			t.Errorf("result for %q has empty Raw - violates the store-raw hard rule (scores.raw is NOT NULL)", r.JobKey)
		}
		if r.EmittedScore < 0 || r.EmittedScore > 100 {
			t.Errorf("result for %q has out-of-range score %v (expected 0-100)", r.JobKey, r.EmittedScore)
		}
		if r.Model != model.Name {
			t.Errorf("result Model = %q, want %q", r.Model, model.Name)
		}
	}
}

func TestOpenAIScorerAuthHeaderValue(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx := context.Background()

	tests := []struct {
		name      string
		apiKey    string
		cred      *fakeTokenCredential
		authScope string
		want      string
		wantErr   bool
	}{
		{name: "no auth configured", want: ""},
		{name: "static key only", apiKey: "sk-local", want: "Bearer sk-local"},
		{
			name:      "AuthScope with working credential prefers token over static key",
			apiKey:    "sk-local",
			cred:      &fakeTokenCredential{token: "fake-aad-token"},
			authScope: "https://cognitiveservices.azure.com/.default",
			want:      "Bearer fake-aad-token",
		},
		{
			name:      "AuthScope with failing credential errors",
			cred:      &fakeTokenCredential{err: errTestCredential},
			authScope: "https://cognitiveservices.azure.com/.default",
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &openaiScorer{logger: logger, apiKey: tt.apiKey}
			if tt.cred != nil {
				s.cred = tt.cred
			}
			got, err := s.authHeaderValue(ctx, ModelConfig{AuthScope: tt.authScope})
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("authHeaderValue: %v", err)
			}
			if got != tt.want {
				t.Errorf("authHeaderValue() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBestEffortScoreEV is a pure-logic test (CLAUDE.md §9 tier 1: no
// network, no fake server) covering the function that decides EVScore vs
// nil in ScoreResult (CLAUDE.md §4.6). content/logprobs below are
// hand-constructed so the token-offset walk in bestEffortScoreEV lands
// exactly on the numeral: `prefix` is the literal bytes up to and including
// `"score":`, so its length IS the byte offset the real digits start at,
// and the second synthetic token is placed to start at that same offset.
func TestBestEffortScoreEV(t *testing.T) {
	const content = `{"key":"a","score":75}`
	const prefix = `{"key":"a","score":` // len 19 == byte offset of "75" in content above

	type candidate struct {
		Token   string  `json:"token"`
		Logprob float64 `json:"logprob"`
	}
	type contentToken struct {
		Token       string      `json:"token"`
		Logprob     float64     `json:"logprob"`
		TopLogprobs []candidate `json:"top_logprobs"`
	}
	marshalLogprobs := func(tokens []contentToken) json.RawMessage {
		b, err := json.Marshal(struct {
			Content []contentToken `json:"content"`
		}{tokens})
		if err != nil {
			t.Fatalf("marshal test logprobs: %v", err)
		}
		return b
	}

	validTokens := []contentToken{
		{Token: prefix},
		{Token: "75", TopLogprobs: []candidate{
			{Token: "75", Logprob: math.Log(0.7)},
			{Token: "74", Logprob: math.Log(0.3)},
		}},
		{Token: "}"},
	}

	tests := []struct {
		name     string
		content  string
		logprobs json.RawMessage
		key      string
		emitted  float64
		wantEV   float64
		wantOK   bool
	}{
		{
			name:     "clean single numeric token with distribution yields weighted EV",
			content:  content,
			logprobs: marshalLogprobs(validTokens),
			key:      "a",
			emitted:  75,
			wantEV:   74.7, // 0.7*75 + 0.3*74
			wantOK:   true,
		},
		{
			name:     "empty logprobs falls back to emitted",
			content:  content,
			logprobs: nil,
			key:      "a",
			emitted:  75,
			wantEV:   75,
			wantOK:   false,
		},
		{
			name:     "key not found in content falls back to emitted",
			content:  content,
			logprobs: marshalLogprobs(validTokens),
			key:      "nonexistent",
			emitted:  75,
			wantEV:   75,
			wantOK:   false,
		},
		{
			name:    "non-numeric token at target offset falls back (multi-token split gap)",
			content: content,
			logprobs: marshalLogprobs([]contentToken{
				{Token: prefix},
				{Token: "AB"},
				{Token: "}"},
			}),
			key:     "a",
			emitted: 75,
			wantEV:  75,
			wantOK:  false,
		},
		{
			name:    "non-numeric candidate in the distribution falls back",
			content: content,
			logprobs: marshalLogprobs([]contentToken{
				{Token: prefix},
				{Token: "75", TopLogprobs: []candidate{
					{Token: "75", Logprob: math.Log(0.7)},
					{Token: "seventy-four", Logprob: math.Log(0.3)},
				}},
				{Token: "}"},
			}),
			key:     "a",
			emitted: 75,
			wantEV:  75,
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := bestEffortScoreEV(tt.content, tt.logprobs, tt.key, tt.emitted)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if math.Abs(got-tt.wantEV) > 0.001 {
				t.Errorf("EV = %v, want %v", got, tt.wantEV)
			}
		})
	}
}

func int64p(v int64) *int64 { return &v }

// TestParseOpenAIUsage is a pure-logic test (no network) covering CLAUDE.md
// §4.2's nullability contract: each itemized field must be nil when its
// provider's response didn't itemize it, and non-nil (even when zero) when
// it did - a present-but-zero count is a real fact, not the same as
// "unknown," so it must never collapse to the nil-cascade case.
func TestParseOpenAIUsage(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Usage
	}{
		{
			name: "both details present",
			raw:  `{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":40},"completion_tokens_details":{"reasoning_tokens":20}}`,
			want: Usage{
				InputUncached: int64p(60),
				CacheRead:     int64p(40),
				Output:        int64p(30),
				Reasoning:     int64p(20),
				Total:         150,
			},
		},
		{
			name: "explicit zero counts still populate (not fabricated as nil)",
			raw:  `{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}`,
			want: Usage{
				InputUncached: int64p(100),
				CacheRead:     int64p(0),
				Output:        int64p(50),
				Reasoning:     int64p(0),
				Total:         150,
			},
		},
		{
			name: "neither details object present - all itemized fields nil, Total still known",
			raw:  `{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}`,
			want: Usage{Total: 150},
		},
		{
			name: "only prompt_tokens_details present - completion-side stays nil independently",
			raw:  `{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":10}}`,
			want: Usage{
				InputUncached: int64p(90),
				CacheRead:     int64p(10),
				Total:         150,
			},
		},
		{
			name: "empty raw yields zero Usage",
			raw:  "",
			want: Usage{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseOpenAIUsage(json.RawMessage(tt.raw))

			if got.Total != tt.want.Total {
				t.Errorf("Total = %v, want %v", got.Total, tt.want.Total)
			}
			checkPtr := func(field string, got, want *int64) {
				t.Helper()
				switch {
				case want == nil && got != nil:
					t.Errorf("%s = %v, want nil", field, *got)
				case want != nil && got == nil:
					t.Errorf("%s = nil, want %v", field, *want)
				case want != nil && got != nil && *got != *want:
					t.Errorf("%s = %v, want %v", field, *got, *want)
				}
			}
			checkPtr("InputUncached", got.InputUncached, tt.want.InputUncached)
			checkPtr("CacheRead", got.CacheRead, tt.want.CacheRead)
			checkPtr("Output", got.Output, tt.want.Output)
			checkPtr("Reasoning", got.Reasoning, tt.want.Reasoning)
			if got.CacheWrite != nil {
				t.Errorf("CacheWrite = %v, want nil (no OpenAI-compatible dialect reports this)", *got.CacheWrite)
			}
			if tt.raw != "" && string(got.Raw) != tt.raw {
				t.Errorf("Raw = %q, want %q (must round-trip verbatim as the backstop)", got.Raw, tt.raw)
			}
		})
	}
}
