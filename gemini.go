package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// geminiScorer is the AWS Scorer impl: it batches every job passed to
// ScoreBatch into a single Gemini API call with a structured-output schema.
type geminiScorer struct {
	logger       *slog.Logger
	client       *http.Client
	apiKey       string
	instructions []byte
}

func newGeminiScorer(logger *slog.Logger, client *http.Client, apiKey string, instructions []byte) *geminiScorer {
	return &geminiScorer{logger: logger, client: client, apiKey: apiKey, instructions: instructions}
}

type scoreResult struct {
	Key       string `json:"key"`
	Score     int    `json:"score"`
	Reasoning string `json:"reasoning"`
}

type geminiRequest struct {
	Contents         []content        `json:"contents"`
	GenerationConfig generationConfig `json:"generationConfig"`
}

type content struct {
	Parts []part `json:"parts"`
}
type part struct {
	Text string `json:"text"`
}

type generationConfig struct {
	ResponseMIMEType string `json:"responseMimeType"`
	ResponseSchema   any    `json:"responseSchema"`
}

type jobPayload struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Company     string `json:"company"`
	Location    string `json:"location"`
	Description string `json:"description"`
}

// temperature is accepted to satisfy the shared Scorer interface but
// intentionally unused: this is the AWS/Gemini path, out of scope for the
// Azure measurement instrument's §4.7 requirement, and Gemini's
// generationConfig here has no temperature knob wired.
func (g *geminiScorer) ScoreBatch(ctx context.Context, jobs []Job, model ModelConfig, temperature float32) ([]ScoreResult, Usage, error) {
	jobsJSON, err := json.Marshal(toPayload(jobs)) // []jobPayload
	if err != nil {
		wrapped := wrapErr("error marshalling jobs", err)
		g.logger.Error("error marshalling jobs for scoring", errAttr(wrapped))
		return nil, Usage{}, wrapped
	}
	var scoreSchema = map[string]any{ //schema for how gemini response comes in
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key":       map[string]any{"type": "string"},
				"score":     map[string]any{"type": "integer"},
				"reasoning": map[string]any{"type": "string"},
			},
			"required":         []string{"key", "score", "reasoning"},
			"propertyOrdering": []string{"key", "score", "reasoning"},
		},
	}
	reqBody := geminiRequest{
		Contents: []content{{Parts: []part{
			{Text: string(g.instructions)},
			{Text: "Jobs to score:\n" + string(jobsJSON)},
		}}},
		GenerationConfig: generationConfig{
			ResponseMIMEType: "application/json",
			ResponseSchema:   scoreSchema,
		},
	}
	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		wrapped := wrapErr("error marshalling request", err)
		g.logger.Error("error marshalling request for scoring", errAttr(wrapped))
		return nil, Usage{}, wrapped
	}
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", model.Name) //WATCH FOR POTENTIAL URL CHANGE THROUGH API UPDATE
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBytes))                 //If you want to use a different gemini model, you can change the model in the URL,
	if err != nil {                                                                                              //But you will also need to adjust batch size, ticker size, and potentially, token budget in handler in main.go
		wrapped := wrapErr("error creating request", err)
		g.logger.Error("error creating request for scoring", errAttr(wrapped))
		return nil, Usage{}, wrapped
	}
	req.Header.Set("x-goog-api-key", g.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		wrapped := wrapErr("error doing request", err)
		g.logger.Error("error doing request for scoring", errAttr(wrapped))
		return nil, Usage{}, wrapped
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		g.logger.Error("non-200 from gemini",
			slog.Int("status", resp.StatusCode),
			slog.String("body", string(body)))
		return nil, Usage{}, &statusError{code: resp.StatusCode}
	}

	var gr struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		// UsageMetadata is captured raw, not itemized: this is the AWS/Gemini
		// path, out of scope for the Azure measurement instrument's
		// per-token capture requirements (CLAUDE.md §1) - AWS's DynamoDB
		// schema has no columns for it. Total/Raw are populated only so
		// this satisfies the shared Scorer interface and main.go's
		// generic throttle/logging code keeps working unchanged.
		UsageMetadata json.RawMessage `json:"usageMetadata"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		wrapped := wrapErr("failed to decode envelope", err)
		g.logger.Error("error decoding response for scoring", errAttr(wrapped))
		return nil, Usage{}, wrapped
	}
	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		traced := traceErrorf("empty response from gemini")
		g.logger.Error("empty response from gemini for scoring", errAttr(traced))
		return nil, Usage{}, traced
	}
	var results []scoreResult
	if err := json.Unmarshal([]byte(gr.Candidates[0].Content.Parts[0].Text), &results); err != nil {
		wrapped := wrapErr("failed to decode scores", err)
		g.logger.Error("failed to decode scores for scoring", errAttr(wrapped))
		return nil, Usage{}, wrapped
	}

	var usageTotal struct {
		TotalTokenCount int `json:"totalTokenCount"`
	}
	json.Unmarshal(gr.UsageMetadata, &usageTotal) // best-effort; zero total on failure is fine, same as before
	usage := Usage{Total: float64(usageTotal.TotalTokenCount), Raw: gr.UsageMetadata}

	return toScoreResults(model.Name, results), usage, nil
}

func toPayload(jobs []Job) []jobPayload {
	out := make([]jobPayload, len(jobs))
	for i, j := range jobs {
		out[i] = jobPayload{
			Key:         j.Key,
			Title:       j.Title,
			Company:     j.Company,
			Location:    j.Location,
			Description: j.Description,
		}
	}
	return out
}

// toScoreResults converts the provider's raw response into the interface's
// ScoreResult shape. Correlating results back to input jobs (and logging any
// job the provider dropped) is the caller's job, done uniformly for every
// Scorer impl by zipScoreEvents rather than here.
func toScoreResults(model string, results []scoreResult) []ScoreResult {
	now := time.Now()
	out := make([]ScoreResult, len(results))
	for i, r := range results {
		out[i] = ScoreResult{
			JobKey:       r.Key,
			Model:        model,
			EmittedScore: float64(r.Score),
			// EVScore left nil: the Gemini/AWS path never requests or parses
			// logprobs, so there is no EV to report (CLAUDE.md §4.6's EV path
			// is best-effort and fires unevenly across the catalog - Gemini
			// is simply a provider where it never fires).
			Reasoning: r.Reasoning,
			ScoredAt:  now,
		}
	}
	return out
}
