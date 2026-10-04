// Package sqs sends jobs to the SQS queues that feed the stateless-jobs
// Lambda: edge extraction and canonicalization, the two job types that are
// pure Go computation with no AWS Batch hop. The Batch-backed job types
// start Step Functions executions directly instead (internal/queue/
// stepfunctions), so they have no queue here.
//
// There is no consumer half: Lambda's SQS event source mapping does the
// polling, and the Lambda's dispatcher (internal/worker/lambda) reads the
// messages this package sends.
package sqs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Client is the narrow SQS capability Store needs, satisfied by the real
// AWS SDK client (see NewClient) or a test double (internal/queue/sqs/mock).
type Client interface {
	// SendMessage publishes body to the queue at queueURL.
	SendMessage(ctx context.Context, queueURL, body string) error
}

// Config maps each job type Store sends to its queue URL. Named fields
// rather than a map, so a missing one is visible where Config is built.
type Config struct {
	EdgeExtractionQueueURL   string
	CanonicalizationQueueURL string
}

// Store sends job runs to SQS.
type Store struct {
	client Client
	cfg    Config
}

// New returns a Store that sends through client, routing each run to the
// queue cfg names for its job type.
func New(client Client, cfg Config) *Store {
	return &Store{client: client, cfg: cfg}
}

// message is the JSON body of every message. The Lambda dispatcher
// unmarshals it; attempt identifies which status-store attempt the run
// reports progress against.
type message struct {
	Type       queue.JobType `json:"type"`
	DocumentID uuid.UUID     `json:"document_id"`
	UserID     uuid.UUID     `json:"user_id"`
	Attempt    int           `json:"attempt"`
}

// Dispatch sends run to its job type's queue.
func (s *Store) Dispatch(ctx context.Context, run queue.JobRun) error {
	queueURL, err := s.queueFor(run.Type)
	if err != nil {
		return err
	}
	body, err := json.Marshal(message{Type: run.Type, DocumentID: run.DocumentID, UserID: run.UserID, Attempt: run.Attempt})
	if err != nil {
		return fmt.Errorf("queue/sqs: marshal %s message for document %s: %w", run.Type, run.DocumentID, err)
	}
	if err := s.client.SendMessage(ctx, queueURL, string(body)); err != nil {
		return fmt.Errorf("queue/sqs: send %s for document %s: %w", run.Type, run.DocumentID, err)
	}
	return nil
}

func (s *Store) queueFor(jobType queue.JobType) (string, error) {
	var url string
	switch jobType {
	case queue.JobTypeEdgeExtraction:
		url = s.cfg.EdgeExtractionQueueURL
	case queue.JobTypeCanonicalization:
		url = s.cfg.CanonicalizationQueueURL
	default:
		return "", fmt.Errorf("queue/sqs: no queue for job type %q", jobType)
	}
	if url == "" {
		return "", fmt.Errorf("queue/sqs: queue URL for %q is not configured", jobType)
	}
	return url, nil
}
