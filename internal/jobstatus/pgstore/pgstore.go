// Package pgstore provides a Postgres-backed jobstatus.Store.
package pgstore

import (
	"context"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Store is a Postgres-backed jobstatus.Store.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) *Store { return &Store{runner: runner} }

// Enqueue implements jobstatus.Writer.
func (s *Store) Enqueue(ctx context.Context, key jobstatus.Key) (jobstatus.Enqueued, error) {
	return jobstatus.Enqueued{}, nil
}

// MarkProcessing implements jobstatus.Writer.
func (s *Store) MarkProcessing(ctx context.Context, key jobstatus.Key, attempt int) error { return nil }

// SetPhase implements jobstatus.Writer.
func (s *Store) SetPhase(ctx context.Context, key jobstatus.Key, attempt int, phase string) error {
	return nil
}

// MarkSucceeded implements jobstatus.Writer.
func (s *Store) MarkSucceeded(ctx context.Context, key jobstatus.Key, attempt int) error { return nil }

// MarkFailed implements jobstatus.Writer.
func (s *Store) MarkFailed(ctx context.Context, key jobstatus.Key, attempt int, reason string) error {
	return nil
}

// ActiveJobsForDocuments implements queue.JobStatusReader.
func (s *Store) ActiveJobsForDocuments(ctx context.Context, userID uuid.UUID, documentIDs []uuid.UUID) (map[uuid.UUID][]queue.JobStatus, error) {
	return nil, nil
}
