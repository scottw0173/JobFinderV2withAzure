package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Job struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Company     string `json:"company"`
	Location    string `json:"location"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Source      string `json:"source"`
	PostedAt    int64  `json:"posted_at"`
	IsRemote    bool   `json:"is_remote,omitempty"`
}

/*type jobKey struct {
	Stablekey string
	PostedAt  int64
}

func (j Job) key() jobKey {
	return jobKey{
		Stablekey: j.createStableKey(),
		PostedAt:  j.PostedAt,
	}
}

type Salary struct {
	Min      float64 `json:"min_salary"`
	Max      float64 `json:"max_salary"`
	Currency string  `json:"currency"`
	Period   string  `json:"period"` // yearly or monthly
}*/

func (j Job) createStableKey() string {
	return strings.Join(strings.Fields(j.Company+j.Title+j.Location), "")
}
func (j Job) createCompositeKey() string {
	return compositeKey(j.createStableKey(), j.PostedAt)
}

// compositeKey is the shared identity key joining a stable key (company +
// title + location) with a posting timestamp - used by Job, SeenJob, and the
// AWS Store's dynamoDBItem alike, so a scraped Job and any stored
// representation always produce byte-for-byte identical keys.
func compositeKey(stablekey string, postedAt int64) string {
	return fmt.Sprintf("%s#%d", stablekey, postedAt)
}

func createSourcesMap(ctx context.Context, a *App) (map[string][]string, error) {

	data, err := a.Config.File(ctx, "sources.json")
	if err != nil {
		a.Logger.Error("cannot read sources.json", errAttr(err))
		return nil, err
	}
	var sources map[string][]string
	if err := json.Unmarshal(data, &sources); err != nil {
		a.Logger.Error("cannot unmarshal sources.json", errAttr(err))
		return nil, err
	}
	return sources, nil
}

func fetchJSON[T any](ctx context.Context, app *App, url string) (T, error) {
	var result T

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		wrapped := wrapErr("failed to create request", err)
		app.Logger.Error("failed to create request", errAttr(wrapped))
		return result, wrapped
	}
	req.Header.Set("Accept", "application/json")

	resp, err := app.Client.Do(req)
	if err != nil {
		wrapped := wrapErr("failed to do request", err)
		app.Logger.Error("failed to do request", errAttr(wrapped))
		return result, wrapped
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		traced := traceErrorf("unexpected status code: %d", resp.StatusCode)
		app.Logger.Error("unexpected status code", slog.Int("status", resp.StatusCode), errAttr(traced))
		return result, traced
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		wrapped := wrapErr("failed to decode response", err)
		app.Logger.Error("failed to decode response", errAttr(wrapped))
		return result, wrapped
	}
	return result, nil
}

func filterJobs(jobs []Job, filter *KeywordFilter) []Job {
	var out []Job
	for _, j := range jobs {
		if j.IsRemote && filter.Matches(j.Title+" "+j.Location) {
			out = append(out, j)
		}
	}
	return out
}

func collect(ctx context.Context, a *App) ([]Job, error) {
	sources, err := createSourcesMap(ctx, a)
	if err != nil {
		return nil, err
	}

	fetchers := map[string]func(context.Context, *App, string) ([]Job, error){
		"greenhouse": fetchGreenhouse,
		"ashby":      fetchAshby,
		"lever":      fetchLever,
	}

	var all []Job
	for provider, companies := range sources {
		fn, ok := fetchers[provider]
		if !ok {
			a.Logger.Warn("no fetcher for provider", "provider", provider)
			continue
		}
		for _, c := range companies {
			jobs, err := fn(ctx, a, c)
			if err != nil {
				a.Logger.Warn("fetch failed", "provider", provider, "company", c, "err", err)
				continue
			}
			all = append(all, jobs...)
		}
	}
	a.Logger.Info("collected jobs", "count", len(all))
	return all, nil
}

/*func rankedJobsToDynamoDBItems(jobs []RankedJob) []DynamoDBItem {
	var items []DynamoDBItem
	for _, job := range jobs {
		item := DynamoDBItem{
			Stablekey: job.createStableKey(),
			PostedAt:  job.PostedAt,
			Score:     job.Score,
			Title:     job.Title,
			Company:   job.Company,
			Location:  job.Location,
			URL:       job.URL,
			Source:    job.Source,
			Reasoning: job.Reasoning,
			LastSeen:  time.Now(),
		}
		items = append(items, item)
	}
	return items
}

func writeResultsToS3(ctx context.Context, a *App, jobs []Job) error {
	data, err := json.Marshal(jobs)
	if err != nil {
		return err
	}
	identifier := fmt.Sprintf("jobs-%s.json", time.Now().Format("2006-01-02T15-04-05"))
	_, err = a.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(a.s3Result),
		Key:         aws.String(identifier),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("failed to write to S3: %w", err)
	}
	return nil
}*/
