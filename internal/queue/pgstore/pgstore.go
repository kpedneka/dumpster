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
//
// max_attempts is set to queue.SingleShotMaxAttempts (1), not the jobs
// table's default (3) -- see that constant's doc for why: this job type
// is a thin wrapper around one AWS Batch submission, and Batch failures
// here are deterministic, not transient, so retrying just wastes compute.
func (s *Store) PublishEntityExtraction(ctx context.Context, evt queue.EntityExtractionRequested) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO jobs (document_id, user_id, job_type, max_attempts)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		evt.DocumentID, evt.UserID, string(queue.JobTypeEntityExtraction), queue.SingleShotMaxAttempts,
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

// PublishRegionClassification inserts a pending region-classification job
// for the given PDF or image document as the alternative ingestion entry
// point to document indexing.
//
// max_attempts is set to queue.SingleShotMaxAttempts (1) -- this job type
// now embeds its chunks via an AWS Batch job (internal/llm/awsbatch) as
// its final step, and a failure there is exactly as deterministic as
// entity extraction's own Batch failures. See that constant's doc for the
// full reasoning; accepted as covering the whole job type even though its
// earlier steps (the /regions HTTP call, DB writes) could in principle
// have more transient failure modes.
func (s *Store) PublishRegionClassification(ctx context.Context, evt queue.RegionClassificationRequested) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO jobs (document_id, user_id, job_type, max_attempts)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		evt.DocumentID, evt.UserID, string(queue.JobTypeRegionClassification), queue.SingleShotMaxAttempts,
	)
	if err != nil {
		return fmt.Errorf("queue: enqueue region classification for document %s: %w", evt.DocumentID, err)
	}
	return nil
}

// PublishCanonicalization inserts a pending canonicalization job for the
// given document. This is a distinct job type from edge extraction so it
// can be queued and re-run independently after an entity re-run completes.
func (s *Store) PublishCanonicalization(ctx context.Context, evt queue.CanonicalizationRequested) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO jobs (document_id, user_id, job_type)
		 VALUES ($1, $2, $3)
		 ON CONFLICT DO NOTHING`,
		evt.DocumentID, evt.UserID, string(queue.JobTypeCanonicalization),
	)
	if err != nil {
		return fmt.Errorf("queue: enqueue canonicalization for document %s: %w", evt.DocumentID, err)
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

// Heartbeat touches jobID's updated_at so ReclaimStale doesn't treat active
// progress as staleness.
func (s *Store) Heartbeat(ctx context.Context, jobID uuid.UUID) error {
	if _, err := s.pool.Exec(ctx, `UPDATE jobs SET updated_at = NOW() WHERE id = $1`, jobID); err != nil {
		return fmt.Errorf("queue: heartbeat %s: %w", jobID, err)
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

// ReclaimStale resets jobs stuck in "processing" for longer than staleAfter
// back to "pending" (incrementing attempts, same as a failed attempt would),
// or dead-letters them directly if that exhausts max_attempts. This is the
// recovery path for a job orphaned by a worker that dies or restarts
// mid-run: Dequeue's claim-then-commit transaction is short, so a crash
// inside Handle() never touches the jobs table again, and Nack (which
// normally dead-letters after enough failures) only fires when Handle()
// actually returns, which a killed process never gets the chance to do.
//
// Known limitation: a job dead-lettered here (rather than via a normal
// Nack) does not trigger the handler's OnFailed callback, since that is
// invoked by Worker.process, not by this package — a document tied to such
// a job stays at its last known status rather than being marked failed.
// This only matters if the exact same job orphans repeatedly across
// multiple worker restarts, exhausting attempts purely through staleness
// reclaims without Handle() ever running to completion or returning a real
// error; recovering from "one crash orphans a job forever" (the bug this
// method exists to fix) does not depend on that edge case.
func (s *Store) ReclaimStale(ctx context.Context, staleAfter time.Duration) (reclaimed, deadLettered int, err error) {
	cutoff := time.Now().Add(-staleAfter)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("queue: reclaim stale begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`SELECT id, attempts, max_attempts FROM jobs
		 WHERE status = 'processing' AND updated_at < $1
		 FOR UPDATE`,
		cutoff,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("queue: reclaim stale select: %w", err)
	}
	type staleJob struct {
		id                    uuid.UUID
		attempts, maxAttempts int
	}
	var stale []staleJob
	for rows.Next() {
		var j staleJob
		if err := rows.Scan(&j.id, &j.attempts, &j.maxAttempts); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("queue: reclaim stale scan: %w", err)
		}
		stale = append(stale, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("queue: reclaim stale rows: %w", err)
	}

	for _, j := range stale {
		newAttempts := j.attempts + 1
		if newAttempts >= j.maxAttempts {
			if _, err := tx.Exec(ctx,
				`UPDATE jobs SET status = 'failed', attempts = $1,
				 last_error = 'reclaimed: worker did not complete this job within the staleness window',
				 updated_at = NOW() WHERE id = $2`,
				newAttempts, j.id,
			); err != nil {
				return 0, 0, fmt.Errorf("queue: reclaim stale dead-letter %s: %w", j.id, err)
			}
			deadLettered++
		} else if _, err := tx.Exec(ctx,
			`UPDATE jobs SET status = 'pending', attempts = $1, updated_at = NOW(), run_at = NOW()
			 WHERE id = $2`,
			newAttempts, j.id,
		); err != nil {
			return 0, 0, fmt.Errorf("queue: reclaim stale requeue %s: %w", j.id, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("queue: reclaim stale commit: %w", err)
	}
	return len(stale) - deadLettered, deadLettered, nil
}

var _ queue.Queue = (*Store)(nil)
