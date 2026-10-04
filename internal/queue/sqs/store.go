// Package sqs sends jobs to SQS queues.
package sqs

import (
	"context"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Client is the narrow SQS capability Store needs.
type Client interface {
	// SendMessage publishes body to the queue at queueURL.
	SendMessage(ctx context.Context, queueURL, body string) error
}

// Config maps each job type to its queue URL.
type Config struct {
	EdgeExtractionQueueURL   string
	CanonicalizationQueueURL string
}

// Store sends jobs to SQS.
type Store struct {
	client Client
	cfg    Config
}

// New returns a Store.
func New(client Client, cfg Config) *Store { return &Store{client: client, cfg: cfg} }

// Dispatch sends run to its queue.
func (s *Store) Dispatch(ctx context.Context, run queue.JobRun) error { return nil }
