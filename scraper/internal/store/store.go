// Package store records which jobs have already been notified, in a
// DynamoDB table keyed on job ID. Each record expires via DynamoDB TTL.
package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/calza27/teacher-job-notify/scraper/internal/edupay"
)

const (
	keyAttr = "job_id"
	ttlAttr = "expires_at" // must match TimeToLiveSpecification in infra/app.yaml

	// TTL is how long a notified job is remembered.
	TTL = 30 * 24 * time.Hour

	maxBatchGet   = 100 // DynamoDB BatchGetItem limit
	maxBatchWrite = 25  // DynamoDB BatchWriteItem limit
	maxRetries    = 5
)

// Store is a seen-jobs table.
type Store struct {
	DB    *dynamodb.Client
	Table string
}

// Seen returns the subset of ids already in the table.
func (s *Store) Seen(ctx context.Context, ids []string) (map[string]bool, error) {
	seen := map[string]bool{}
	for chunk := range chunks(ids, maxBatchGet) {
		keys := make([]map[string]types.AttributeValue, len(chunk))
		for i, id := range chunk {
			keys[i] = map[string]types.AttributeValue{keyAttr: &types.AttributeValueMemberS{Value: id}}
		}
		req := map[string]types.KeysAndAttributes{
			s.Table: {Keys: keys, ProjectionExpression: aws.String(keyAttr)},
		}
		for attempt := 0; len(req) > 0; attempt++ {
			if err := backoff(ctx, attempt); err != nil {
				return nil, fmt.Errorf("batch get: %w", err)
			}
			out, err := s.DB.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: req})
			if err != nil {
				return nil, fmt.Errorf("batch get: %w", err)
			}
			for _, item := range out.Responses[s.Table] {
				if v, ok := item[keyAttr].(*types.AttributeValueMemberS); ok {
					seen[v.Value] = true
				}
			}
			req = out.UnprocessedKeys
		}
	}
	return seen, nil
}

// MarkSeen records the jobs so they aren't notified again for TTL.
func (s *Store) MarkSeen(ctx context.Context, jobs []edupay.Job, now time.Time) error {
	expires := strconv.FormatInt(now.Add(TTL).Unix(), 10)
	notified := now.UTC().Format(time.RFC3339)

	for chunk := range chunks(jobs, maxBatchWrite) {
		writes := make([]types.WriteRequest, len(chunk))
		for i, j := range chunk {
			writes[i] = types.WriteRequest{PutRequest: &types.PutRequest{Item: map[string]types.AttributeValue{
				keyAttr:       &types.AttributeValueMemberS{Value: j.ID},
				"title":       &types.AttributeValueMemberS{Value: j.Title},
				"school":      &types.AttributeValueMemberS{Value: j.School},
				"notified_at": &types.AttributeValueMemberS{Value: notified},
				ttlAttr:       &types.AttributeValueMemberN{Value: expires},
			}}}
		}
		req := map[string][]types.WriteRequest{s.Table: writes}
		for attempt := 0; len(req) > 0; attempt++ {
			if err := backoff(ctx, attempt); err != nil {
				return fmt.Errorf("batch write: %w", err)
			}
			out, err := s.DB.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: req})
			if err != nil {
				return fmt.Errorf("batch write: %w", err)
			}
			req = out.UnprocessedItems
		}
	}
	return nil
}

func chunks[T any](s []T, n int) func(func([]T) bool) {
	return func(yield func([]T) bool) {
		for i := 0; i < len(s); i += n {
			if !yield(s[i:min(i+n, len(s))]) {
				return
			}
		}
	}
}

// backoff waits before retrying unprocessed items. Attempt 0 doesn't wait.
func backoff(ctx context.Context, attempt int) error {
	if attempt == 0 {
		return nil
	}
	if attempt > maxRetries {
		return fmt.Errorf("unprocessed items after %d retries", maxRetries)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(50<<attempt) * time.Millisecond):
		return nil
	}
}
