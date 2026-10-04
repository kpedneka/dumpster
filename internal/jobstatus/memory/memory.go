// Package memory provides an in-memory jobstatus.Store for use in tests.
package memory

import (
	"context"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Store is an in-memory jobstatus.Store.
type Store struct{}

// New returns an empty Store.
func New() *Store { return &Store{} }

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
