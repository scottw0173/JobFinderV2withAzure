package main

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// awsConfigSource reads file-shaped config from S3 and reports the AWS-side
// policy: score once, skip already-scored jobs, a single Gemini model.
type awsConfigSource struct {
	client    *s3.Client
	bucket    string
	modelName string
}

func newAWSConfigSource(client *s3.Client, bucket, modelName string) *awsConfigSource {
	return &awsConfigSource{client: client, bucket: bucket, modelName: modelName}
}

func (c *awsConfigSource) File(ctx context.Context, name string) ([]byte, error) {
	output, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(name),
	})
	if err != nil {
		return nil, wrapErr(fmt.Sprintf("get s3 object %s", name), err)
	}
	defer output.Body.Close()
	data, err := io.ReadAll(output.Body)
	if err != nil {
		return nil, wrapErr(fmt.Sprintf("read s3 object %s", name), err)
	}
	return data, nil
}

// TPM/RPM here reuse this codebase's own prior hardcoded throttle values
// (a flat 200000-token/60s budget and a 5s request ticker, ~12 req/min) as
// raw inputs to the shared derated formula in newModelThrottle - not a claim
// about Gemini's real published quota, just this repo's already-trusted
// numbers re-expressed so handler()'s throttle gate doesn't refuse to score
// the AWS path (CLAUDE.md §8's formula now applies uniformly to both
// clouds). Net effect is a slightly more conservative AWS throttle than
// before (75% margin applied where none existed previously).
//
// Protocol is set for documentation only (CLAUDE.md §6/§7): app.Scorer is
// dispatched directly for AWS, never routed through app.Scorers/model.Protocol,
// but ModelConfig should still capture the true condition.
func (c *awsConfigSource) Models(ctx context.Context) ([]ModelConfig, error) {
	return []ModelConfig{{Name: c.modelName, TPM: 200000, RPM: 12, Protocol: "gemini"}}, nil
}

func (c *awsConfigSource) RescoreEveryRun() bool {
	return false
}

// Temperature is unused on the AWS path: geminiScorer never sends it (the
// Azure measurement instrument's §4.7 requirement doesn't apply here).
func (c *awsConfigSource) Temperature() float32 {
	return 0
}

// BatchSize stays fixed: the sweep (CLAUDE.md §4.4/§4.5) is an Azure-only
// concept. Preserves the batch size AWS/Gemini has always used.
func (c *awsConfigSource) BatchSize() int {
	return 5
}

// ContributorID/ResumeID/ConfigID are unused on the AWS path: multi-
// contributor identity (CLAUDE.md §10) is an Azure measurement-instrument
// concept, and AWS's DynamoDB store has no columns for it.
func (c *awsConfigSource) ContributorID() string { return "" }
func (c *awsConfigSource) ResumeID() string      { return "" }
func (c *awsConfigSource) ConfigID() string      { return "" }

// RunMode is always "main" on AWS: the floor/main run-kind split (CLAUDE.md
// §2) is an Azure measurement-instrument concept.
func (c *awsConfigSource) RunMode() string { return "main" }

// The fixed score-stratified 30-job panel is an Azure-only measurement-
// instrument concept; AWS's single always-fresh Gemini pass has no panel
// notion. PanelEnabled() is hardcoded false and, like every method below,
// never reads env at all - AWS behavior is completely unaffected by any of
// these knobs.
func (c *awsConfigSource) PanelEnabled() bool                 { return false }
func (c *awsConfigSource) PanelSize() int                     { return 0 }
func (c *awsConfigSource) ScreeningModel() string             { return "" }
func (c *awsConfigSource) ScoreBands() ([]ScoreBand, error)   { return nil, nil }
func (c *awsConfigSource) BandTargets() ([]BandTarget, error) { return nil, nil }
func (c *awsConfigSource) PanelSeed() int64                   { return 0 }
func (c *awsConfigSource) MaxPerCompany() int                 { return 0 }
func (c *awsConfigSource) ActivePanelID() string              { return "" }
func (c *awsConfigSource) RebuildPanel() bool                 { return false }
