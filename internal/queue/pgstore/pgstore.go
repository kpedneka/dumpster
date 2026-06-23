// Package pgstore provides a Postgres-backed queue.Queue using
// SELECT ... FOR UPDATE SKIP LOCKED for concurrent-safe job dequeuing.
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Store is a Postgres-backed implementation of queue.Queue.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store backed by pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// PublishDocumentUploaded inserts a pending document-indexing job for the
// given document. If a pending or processing job of that type already
// exists for the document the insert is silently skipped, making enqueue
// idempotent.
func (s *Store) PublishDocumentUploaded(ctx context.Context, evt queue.DocumentUploaded) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO jobs (document_id, user_id, job_type)
		 VALUES ($1, $2, $3)
		 ON CONFLICT DO NOTHING`,
		evt.DocumentID, evt.UserID, string(queue.JobTypeDocumentIndexing),
	)
	if err != nil {
		return fmt.Errorf("queue: enqueue document %s: %w", evt.DocumentID, err)
	}
	return nil
}

// PublishEntityExtraction inserts a pending entity-extraction job for the
// given document. This is a distinct job type from document indexing so it
// can be queued and re-run independently — e.g. after the entity type-set
// config changes — without re-chunking or re-embedding. If a pending or
// processing entity-extraction job already exists for the document the
// insert is silently skipped, making enqueue idempotent.
func (s *Store) PublishEntityExtraction(ctx context.Context, evt queue.EntityExtractionRequested) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO jobs (document_id, user_id, job_type)
		 VALUES ($1, $2, $3)
		 ON CONFLICT DO NOTHING`,
		evt.DocumentID, evt.UserID, string(queue.JobTypeEntityExtraction),
	)
	if err != nil {
		return fmt.Errorf("queue: enqueue entity extraction for document %s: %w", evt.DocumentID, err)
	}
	return nil
}

// PublishEdgeExtraction inserts a pending edge-extraction job for the given
// document. This is a distinct job type from entity extraction so it can be
// queued and re-run independently after an entity re-run completes.
func (s *Store) PublishEdgeExtraction(ctx context.Context, evt queue.EdgeExtractionRequested) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO jobs (document_id, user_id, job_type)
		 VALUES ($1, $2, $3)
		 ON CONFLICT DO NOTHING`,
		evt.DocumentID, evt.UserID, string(queue.JobTypeEdgeExtraction),
	)
	if err != nil {
		return fmt.Errorf("queue: enqueue edge extraction for document %s: %w", evt.DocumentID, err)
	}
	return nil
}

// Dequeue claims the next available job using SELECT FOR UPDATE SKIP LOCKED.
// Returns ErrNoJobs when no pending jobs are ready to run.
func (s *Store) Dequeue(ctx context.Context) (*queue.Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("queue: dequeue begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var j queue.Job
	var jobType string
	err = tx.QueryRow(ctx,
		`SELECT id, document_id, user_id, job_type, attempts, max_attempts
		 FROM jobs
		 WHERE status = 'pending' AND run_at <= NOW()
		 ORDER BY created_at
		 LIMIT 1
		 FOR UPDATE SKIP LOCKED`,
	).Scan(&j.ID, &j.DocumentID, &j.UserID, &jobType, &j.Attempts, &j.MaxAttempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, queue.ErrNoJobs
		}
		return nil, fmt.Errorf("queue: dequeue select: %w", err)
	}
	j.Type = queue.JobType(jobType)

	if _, err := tx.Exec(ctx,
		`UPDATE jobs SET status = 'processing', updated_at = NOW() WHERE id = $1`,
		j.ID,
	); err != nil {
		return nil, fmt.Errorf("queue: dequeue update: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("queue: dequeue commit: %w", err)
	}
	return &j, nil
}

// Ack removes a successfully processed job from the queue.
func (s *Store) Ack(ctx context.Context, jobID uuid.UUID) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM jobs WHERE id = $1`, jobID); err != nil {
		return fmt.Errorf("queue: ack %s: %w", jobID, err)
	}
	return nil
}

// Nack records a processing failure. On the final attempt the job is
// dead-lettered (status = 'failed') and the function returns (true, nil).
// Otherwise the job is rescheduled with exponential backoff and returns
// (false, nil).
func (s *Store) Nack(ctx context.Context, jobID uuid.UUID, reason error) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("queue: nack begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var attempts, maxAttempts int
	if err := tx.QueryRow(ctx,
		`SELECT attempts, max_attempts FROM jobs WHERE id = $1 FOR UPDATE`,
		jobID,
	).Scan(&attempts, &maxAttempts); err != nil {
		return false, fmt.Errorf("queue: nack select %s: %w", jobID, err)
	}

	newAttempts := attempts + 1
	lastErr := ""
	if reason != nil {
		lastErr = reason.Error()
	}

	deadLettered := newAttempts >= maxAttempts
	if deadLettered {
		if _, err := tx.Exec(ctx,
			`UPDATE jobs SET status = 'failed', attempts = $1, last_error = $2, updated_at = NOW() WHERE id = $3`,
			newAttempts, lastErr, jobID,
		); err != nil {
			return false, fmt.Errorf("queue: nack dead-letter %s: %w", jobID, err)
		}
	} else {
		backoff := time.Duration(math.Pow(2, float64(newAttempts))) * time.Second
		if _, err := tx.Exec(ctx,
			`UPDATE jobs SET status = 'pending', attempts = $1, last_error = $2, run_at = $3, updated_at = NOW() WHERE id = $4`,
			newAttempts, lastErr, time.Now().Add(backoff), jobID,
		); err != nil {
			return false, fmt.Errorf("queue: nack reschedule %s: %w", jobID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("queue: nack commit %s: %w", jobID, err)
	}
	return deadLettered, nil
}

var _ queue.Queue = (*Store)(nil)
