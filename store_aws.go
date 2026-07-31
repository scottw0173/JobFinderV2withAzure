package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// awsStore is the AWS Store impl: one DynamoDB item per job, upserted and
// overwritten on every write - it holds the latest score, not a history of
// scoring events (unlike the Azure/Postgres impl, which appends one row per
// event).
type awsStore struct {
	logger    *slog.Logger
	client    *dynamodb.Client
	tableName string
}

func newAWSStore(logger *slog.Logger, client *dynamodb.Client, tableName string) *awsStore {
	return &awsStore{logger: logger, client: client, tableName: tableName}
}

type dynamoDBItem struct {
	Stablekey  string    `dynamodbav:"stablekey"`
	PostedAt   int64     `dynamodbav:"posted_at"`
	Title      string    `dynamodbav:"title"`
	Company    string    `dynamodbav:"company"`
	Score      float64   `dynamodbav:"score"`
	Reasoning  string    `dynamodbav:"reasoning"`
	Location   string    `dynamodbav:"location"`
	URL        string    `dynamodbav:"url"`
	Source     string    `dynamodbav:"source"`
	HasApplied bool      `dynamodbav:"has_applied"`
	LastSeen   time.Time `dynamodbav:"last_seen"`
}

func (it dynamoDBItem) compositeKey() string {
	return compositeKey(it.Stablekey, it.PostedAt)
}

// contributorID/resumeID/configID/instructionsVersion: CLAUDE.md §10
// multi-contributor identity is an Azure-instrument concept - this DynamoDB
// store has no columns for it and never will (AWS is the solo-built
// portfolio piece, §1). Accepted and ignored to satisfy the shared Store
// interface.
func (s *awsStore) RecordScores(ctx context.Context, events []ScoringEvent, contributorID, resumeID, configID, instructionsVersion string) error {
	const batchSize = 20
	for i := 0; i < len(events); i += batchSize {
		end := min(i+batchSize, len(events))
		reqs := make([]types.WriteRequest, 0, end-i)
		for _, e := range events[i:end] {
			// DynamoDB holds one overwritten "latest score" per job, not the
			// two-column emitted/EV history the Azure measurement store
			// keeps (CLAUDE.md §4.6) - prefer the EV when the provider
			// produced one, else fall back to the emitted number.
			score := e.Result.EmittedScore
			if e.Result.EVScore != nil {
				score = *e.Result.EVScore
			}
			item, err := attributevalue.MarshalMap(dynamoDBItem{
				Stablekey: e.Job.createStableKey(),
				PostedAt:  e.Job.PostedAt,
				Score:     score,
				Title:     e.Job.Title,
				Company:   e.Job.Company,
				Location:  e.Job.Location,
				URL:       e.Job.URL,
				Source:    e.Job.Source,
				Reasoning: e.Result.Reasoning,
				LastSeen:  time.Now(),
			})
			if err != nil {
				s.logger.Error("failed to marshal item", errAttr(err), slog.String("job", e.Job.Key))
				continue
			}
			reqs = append(reqs, types.WriteRequest{PutRequest: &types.PutRequest{Item: item}})
		}
		if _, err := s.client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]types.WriteRequest{s.tableName: reqs},
		}); err != nil {
			s.logger.Error("failed to write batch", errAttr(err))
			continue
		}
	}
	return nil
}

func (s *awsStore) SeenJobs(ctx context.Context) ([]SeenJob, error) {
	var items []dynamoDBItem
	p := dynamodb.NewScanPaginator(s.client, &dynamodb.ScanInput{
		TableName:            &s.tableName,
		ProjectionExpression: aws.String("stablekey, posted_at, has_applied, last_seen"),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, wrapErr("error scanning jobs table", err)
		}
		var batch []dynamoDBItem
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &batch); err != nil {
			return nil, wrapErr("error unmarshalling scan page", err)
		}
		items = append(items, batch...)
	}
	seen := make([]SeenJob, len(items))
	for i, it := range items {
		seen[i] = SeenJob{
			Stablekey:  it.Stablekey,
			PostedAt:   it.PostedAt,
			HasApplied: it.HasApplied,
			LastSeen:   it.LastSeen,
		}
	}
	return seen, nil
}

func (s *awsStore) BumpLastSeen(ctx context.Context, items []SeenJob, now time.Time) error {
	nowStr := now.UTC().Format(time.RFC3339Nano)
	for _, it := range items {
		_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName:        aws.String(s.tableName),
			Key:              itemKey(it.Stablekey, it.PostedAt),
			UpdateExpression: aws.String("SET last_seen = :n"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":n": &types.AttributeValueMemberS{Value: nowStr},
			},
		})
		if err != nil {
			return wrapErr(fmt.Sprintf("bump %s", it.compositeKey()), err)
		}
	}
	return nil
}

func (s *awsStore) DeleteAged(ctx context.Context, items []SeenJob) (int, error) {
	deleted := 0
	for i := 0; i < len(items); i += 25 {
		end := min(i+25, len(items))
		reqs := make([]types.WriteRequest, 0, end-i)
		for _, it := range items[i:end] {
			reqs = append(reqs, types.WriteRequest{
				DeleteRequest: &types.DeleteRequest{Key: itemKey(it.Stablekey, it.PostedAt)},
			})
		}
		out, err := s.client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]types.WriteRequest{s.tableName: reqs},
		})
		if err != nil {
			return deleted, wrapErr(fmt.Sprintf("delete batch at %d", i), err)
		}
		deleted += (end - i) - len(out.UnprocessedItems[s.tableName])
	}
	return deleted, nil
}

func (s *awsStore) ExportRows(ctx context.Context) ([]ExportRow, error) {
	var items []dynamoDBItem
	p := dynamodb.NewScanPaginator(s.client, &dynamodb.ScanInput{
		TableName: &s.tableName,
		ProjectionExpression: aws.String(
			"stablekey, posted_at, has_applied, last_seen, title, company, score, reasoning, #loc, #site"),
		ExpressionAttributeNames: map[string]string{
			"#loc":  "location",
			"#site": "url",
		},
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, wrapErr("error scanning jobs table", err)
		}
		var batch []dynamoDBItem
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &batch); err != nil {
			return nil, wrapErr("error unmarshalling scan page", err)
		}
		items = append(items, batch...)
	}
	rows := make([]ExportRow, len(items))
	for i, it := range items {
		rows[i] = ExportRow{
			Stablekey:  it.Stablekey,
			PostedAt:   it.PostedAt,
			Title:      it.Title,
			Company:    it.Company,
			Score:      it.Score,
			Reasoning:  it.Reasoning,
			Location:   it.Location,
			URL:        it.URL,
			HasApplied: it.HasApplied,
		}
	}
	return rows, nil
}

// The fixed score-stratified 30-job panel is an Azure-only measurement-
// instrument concept (CLAUDE.md); PanelEnabled() is always false on AWS
// (config_aws.go), so handler() never calls either of these on awsStore.
// They exist only to satisfy the shared Store interface.
func (s *awsStore) BuildPanel(ctx context.Context, seed int64, selection []PanelJob) (string, error) {
	return "", traceErrorf("BuildPanel is not supported on the AWS store")
}

func (s *awsStore) ActivePanel(ctx context.Context, panelID string) ([]Job, string, bool, error) {
	return nil, "", false, traceErrorf("ActivePanel is not supported on the AWS store")
}

func itemKey(stablekey string, postedAt int64) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"stablekey": &types.AttributeValueMemberS{Value: stablekey},
		"posted_at": &types.AttributeValueMemberN{Value: strconv.FormatInt(postedAt, 10)},
	}
}
