package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// openaiScorer talks to any OpenAI-compatible /chat/completions endpoint -
// local Ollama today, real Azure OpenAI/Foundry/Fireworks later (only
// baseURL/auth change; this HTTP call shape is designed to need no further
// edits when the real account arrives). Batches every job passed to
// ScoreBatch into one HTTP call, mirroring geminiScorer. BaseURL is supplied
// per model (CLAUDE.md §6/§12), not fixed on this struct, since a mixed
// panel can have native Foundry and Fireworks-served deployments at
// different endpoints under the same protocol.
type openaiScorer struct {
	logger       *slog.Logger
	client       *http.Client
	apiKey       string                 // static fallback (empty for local Ollama - no auth; a real key for a static-key endpoint)
	cred         azcore.TokenCredential // keyless auth (CLAUDE.md §9); nil until wired, or when unused (e.g. tests)
	instructions []byte
}

func newOpenAIScorer(logger *slog.Logger, client *http.Client, apiKey string, cred azcore.TokenCredential, instructions []byte) *openaiScorer {
	return &openaiScorer{logger: logger, client: client, apiKey: apiKey, cred: cred, instructions: instructions}
}

// scoreFormatOverride re-expresses instructions.md's inherited "0 to 10
// integer" OUTPUT convention (shared prose, not code) on the 0-100 float
// scale CLAUDE.md wants for the Azure measurement instrument. Layered onto
// the request rather than editing instructions.md, which is per-candidate
// user content, not code.
const scoreFormatOverride = `

FORMAT OVERRIDE (applies over any scale mentioned above): express the final
score as a number from 0 to 100, not 0 to 10. Convert proportionally (a
7-out-of-10 judgment becomes 70) - do not invent precision the rubric above
doesn't support; one decimal place is the most you should ever need.`

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type jsonSchemaFormat struct {
	Type       string         `json:"type"` // "json_schema"
	JSONSchema jsonSchemaSpec `json:"json_schema"`
}

type jsonSchemaSpec struct {
	Name   string `json:"name"`
	Strict bool   `json:"strict"`
	Schema any    `json:"schema"`
}

type chatCompletionRequest struct {
	Model          string           `json:"model"`
	Messages       []chatMessage    `json:"messages"`
	Temperature    float32          `json:"temperature"`
	ResponseFormat jsonSchemaFormat `json:"response_format"`
	Logprobs       bool             `json:"logprobs,omitempty"`
	TopLogprobs    int              `json:"top_logprobs,omitempty"`
}

// openaiScoreItem is one entry of the model's "results" array. Score is a
// float - CLAUDE.md's hard rule for Azure (gemini.go's int Score stays as-is
// for AWS; this is a deliberate divergence, not a bug to copy).
type openaiScoreItem struct {
	Key       string  `json:"key"`
	Score     float64 `json:"score"`
	Reasoning string  `json:"reasoning"`
}

// chatCompletionResponse is the OpenAI-compatible envelope both Ollama and
// (later) an OpenAI-compatible Azure OpenAI surface return.
type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Logprobs json.RawMessage `json:"logprobs"` // captured verbatim; shape inspected only inside bestEffortScoreEV
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"` // captured verbatim; parsed by parseOpenAIUsage (CLAUDE.md §4.2)
}

// openaiUsage is the typed shape parseOpenAIUsage parses out of
// chatCompletionResponse.Usage. The *_details sub-objects are pointers so
// their absence is distinguishable from a present-but-zero count.
type openaiUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// parseOpenAIUsage builds a Usage from a raw OpenAI-compatible "usage"
// object (CLAUDE.md §4.2). Cache and reasoning fields are gated
// independently by their own *_details sub-object: absence of that
// sub-object leaves both fields it would have populated as nil - the
// undifferentiated prompt_tokens/completion_tokens total is known, but
// "not itemized" isn't the same fact as "itemized as zero," so no inference
// is made across a missing sub-object. Raw is set unconditionally as the
// backstop even when parsing the typed fields fails or the object is empty.
func parseOpenAIUsage(raw json.RawMessage) Usage {
	usage := Usage{Raw: raw}
	if len(raw) == 0 {
		return usage
	}
	var u openaiUsage
	if err := json.Unmarshal(raw, &u); err != nil {
		return usage
	}
	usage.Total = float64(u.TotalTokens)

	if u.PromptTokensDetails != nil {
		cacheRead := int64(u.PromptTokensDetails.CachedTokens)
		uncached := int64(u.PromptTokens - u.PromptTokensDetails.CachedTokens)
		usage.CacheRead = &cacheRead
		usage.InputUncached = &uncached
	}
	if u.CompletionTokensDetails != nil {
		reasoning := int64(u.CompletionTokensDetails.ReasoningTokens)
		output := int64(u.CompletionTokens - u.CompletionTokensDetails.ReasoningTokens)
		usage.Reasoning = &reasoning
		usage.Output = &output
	}
	// CacheWrite stays nil: no OpenAI-compatible dialect in the current
	// panel exposes a cache-write count (that's the Anthropic Messages
	// API's cache_creation_input_tokens - deferred with the third protocol
	// scorer, CLAUDE.md §12).
	return usage
}

func (s *openaiScorer) ScoreBatch(ctx context.Context, jobs []Job, model ModelConfig, temperature float32) ([]ScoreResult, Usage, error) {
	jobsJSON, err := json.Marshal(toPayload(jobs)) // gemini.go's jobPayload/toPayload - reused, not redefined
	if err != nil {
		wrapped := wrapErr("error marshalling jobs", err)
		s.logger.Error("error marshalling jobs for scoring", errAttr(wrapped))
		return nil, Usage{}, wrapped
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"results": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"key":       map[string]any{"type": "string"},
						"score":     map[string]any{"type": "number"},
						"reasoning": map[string]any{"type": "string"},
					},
					"required":             []string{"key", "score", "reasoning"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"results"},
		"additionalProperties": false,
	}
	// Top level is an object wrapping a "results" array (not a bare array) -
	// matches OpenAI's strict-mode constraint that the schema root must be
	// an object; keeps the door open for step 7 unchanged.

	reqBody := chatCompletionRequest{
		Model: model.Name,
		Messages: []chatMessage{
			{Role: "system", Content: string(s.instructions) + scoreFormatOverride},
			{Role: "user", Content: "Jobs to score:\n" + string(jobsJSON)},
		},
		Temperature: temperature, // run-level (CLAUDE.md §4.7), not per-model - set via AZURE_TEMPERATURE
		ResponseFormat: jsonSchemaFormat{
			Type:       "json_schema",
			JSONSchema: jsonSchemaSpec{Name: "job_scores", Strict: true, Schema: schema},
		},
	}
	if model.WantLogprobs {
		reqBody.Logprobs = true
		reqBody.TopLogprobs = 5
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		wrapped := wrapErr("error marshalling request", err)
		s.logger.Error("error marshalling request for scoring", errAttr(wrapped))
		return nil, Usage{}, wrapped
	}

	// BaseURL is per-model (CLAUDE.md §6/§12), not a field on s: native
	// Foundry and Fireworks-served deployments can sit at different
	// endpoints under the same protocol within one run.
	url := strings.TrimSuffix(model.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBytes))
	if err != nil {
		wrapped := wrapErr("error creating request", err)
		s.logger.Error("error creating request for scoring", errAttr(wrapped))
		return nil, Usage{}, wrapped
	}
	req.Header.Set("Content-Type", "application/json")
	// OpenAI-compatible convention: Authorization: Bearer, whether the value
	// is a static key or an AAD token (CLAUDE.md §9). Real Azure OpenAI's
	// native surface may want an "api-key" header and a
	// /openai/deployments/{deployment}/... path instead - that request-shape
	// reconciliation is launch-day VERIFY work (§12), not guessed at here.
	authHeader, err := s.authHeaderValue(ctx, model)
	if err != nil {
		wrapped := wrapErr("resolving auth for scoring", err)
		s.logger.Error("error resolving auth for scoring", errAttr(wrapped))
		return nil, Usage{}, wrapped
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		wrapped := wrapErr("error doing request", err)
		s.logger.Error("error doing request for scoring", errAttr(wrapped))
		return nil, Usage{}, wrapped
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		s.logger.Error("non-200 from openai-compatible endpoint",
			slog.Int("status", resp.StatusCode), slog.String("body", string(body)))
		return nil, Usage{}, &statusError{code: resp.StatusCode} // classify() handles 5xx retry, unchanged
	}

	var cr chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		wrapped := wrapErr("failed to decode envelope", err)
		s.logger.Error("error decoding response for scoring", errAttr(wrapped))
		return nil, Usage{}, wrapped
	}
	if len(cr.Choices) == 0 {
		traced := traceErrorf("empty response from openai-compatible endpoint")
		s.logger.Error("empty response for scoring", errAttr(traced))
		return nil, Usage{}, traced
	}

	var envelope struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal([]byte(cr.Choices[0].Message.Content), &envelope); err != nil {
		wrapped := wrapErr("failed to decode scores", err)
		s.logger.Error("failed to decode scores for scoring", errAttr(wrapped))
		return nil, Usage{}, wrapped
	}

	logprobs := cr.Choices[0].Logprobs
	usage := parseOpenAIUsage(cr.Usage)
	now := time.Now()
	out := make([]ScoreResult, 0, len(envelope.Results))
	for _, raw := range envelope.Results {
		var item openaiScoreItem
		if err := json.Unmarshal(raw, &item); err != nil {
			// Per-item, not batch-fatal: zipScoreEvents already logs+skips
			// any job with no matching JobKey, so dropping here just routes
			// into that existing path.
			s.logger.Warn("dropping malformed score item", errAttr(wrapErr("unmarshal score item", err)))
			continue
		}

		var evScore *float64
		var lp json.RawMessage
		if model.WantLogprobs && len(logprobs) > 0 {
			// Same batch-level logprobs blob is stored verbatim on every
			// result in this batch (see bestEffortScoreEV doc comment) -
			// "store raw" is satisfied even though it's duplicated per row.
			lp = logprobs
			if ev, ok := bestEffortScoreEV(cr.Choices[0].Message.Content, logprobs, item.Key, item.Score); ok {
				evScore = &ev
			}
		}

		out = append(out, ScoreResult{
			JobKey:       item.Key,
			Model:        model.Name,
			Deployment:   model.Deployment,
			EmittedScore: item.Score,
			EVScore:      evScore,
			Usage:        usage,
			Reasoning:    item.Reasoning,
			Raw:          raw, // verbatim per-item bytes as emitted - never empty for anything appended here
			Logprobs:     lp,
			ScoredAt:     now,
			Temperature:  float64(temperature),
		})
	}
	return out, usage, nil
}

// authHeaderValue resolves this request's Authorization header: token-based
// (AAD, CLAUDE.md §9) if the model declares an AuthScope and a credential is
// available, else the scorer's static apiKey (possibly empty - local Ollama
// needs no auth at all). Returns "" for "no header."
func (s *openaiScorer) authHeaderValue(ctx context.Context, model ModelConfig) (string, error) {
	if model.AuthScope != "" && s.cred != nil {
		token, err := s.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{model.AuthScope}})
		if err != nil {
			return "", wrapErr("fetching AAD token for scoring", err)
		}
		return "Bearer " + token.Token, nil
	}
	if s.apiKey != "" {
		return "Bearer " + s.apiKey, nil
	}
	return "", nil
}

// bestEffortScoreEV attempts a probability-weighted expected value over the
// score token(s) for one job's result inside a BATCHED completion (ScoreBatch
// scores up to 5 jobs per HTTP call, so content/logprobsRaw cover every
// job's output together, not just this one).
//
// First-pass heuristic, expected to fall back to the emitted number often in
// practice. Falls back (returns ok=false; caller keeps the emitted number)
// when: logprobsRaw is empty or doesn't parse into the expected token shape;
// the job's key or a "score" field after it can't be found in content; the
// token at that offset isn't a single clean numeric token (a multi-token
// split numeral is a known gap, not attempted here); or any top_logprobs
// candidate at that token isn't itself numeric - treated as unreliable, not
// silently excluded. Refine after testing against real local-model output.
func bestEffortScoreEV(content string, logprobsRaw json.RawMessage, key string, emitted float64) (float64, bool) {
	var lp struct {
		Content []struct {
			Token       string  `json:"token"`
			Logprob     float64 `json:"logprob"`
			TopLogprobs []struct {
				Token   string  `json:"token"`
				Logprob float64 `json:"logprob"`
			} `json:"top_logprobs"`
		} `json:"content"`
	}
	if err := json.Unmarshal(logprobsRaw, &lp); err != nil || len(lp.Content) == 0 {
		return emitted, false
	}

	keyIdx := strings.Index(content, `"`+key+`"`)
	if keyIdx < 0 {
		return emitted, false
	}
	scoreLabel := strings.Index(content[keyIdx:], `"score"`)
	if scoreLabel < 0 {
		return emitted, false
	}
	targetOffset := keyIdx + scoreLabel + len(`"score":`)

	offset, tokenIdx := 0, -1
	for i, t := range lp.Content {
		if offset >= targetOffset {
			tokenIdx = i
			break
		}
		offset += len(t.Token)
	}
	for tokenIdx >= 0 && tokenIdx < len(lp.Content) && strings.TrimSpace(lp.Content[tokenIdx].Token) == "" {
		tokenIdx++
	}
	if tokenIdx < 0 || tokenIdx >= len(lp.Content) {
		return emitted, false
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(lp.Content[tokenIdx].Token), 64); err != nil {
		return emitted, false // not a clean single numeric token
	}

	var weighted, weightSum float64
	for _, cand := range lp.Content[tokenIdx].TopLogprobs {
		v, err := strconv.ParseFloat(strings.TrimSpace(cand.Token), 64)
		if err != nil {
			return emitted, false // a non-numeric candidate makes the distribution unreliable
		}
		w := math.Exp(cand.Logprob)
		weighted += w * v
		weightSum += w
	}
	if weightSum == 0 {
		return emitted, false
	}
	return weighted / weightSum, true
}
