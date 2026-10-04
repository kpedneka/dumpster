// Package pgstore provides a Postgres-backed jobstatus.Store over the
// document_job_status table (migrations/031_document_job_status.sql).
//
// Every method runs through a db.TxRunner, so in production (rls.TxRunner)
// the context must carry the user's identity and row-level security scopes
// each query to that tenant, on top of the explicit user_id filters. The
// state rules come from jobstatus.Enqueue and jobstatus.Transition: each
// write locks the row, applies the rule in Go, and writes the result back
// in the same transaction.
package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Store is a Postgres-backed jobstatus.Store.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) *Store {
	return &Store{runner: runner}
}

// Enqueue implements jobstatus.Writer. The first enqueue for a key is a
// plain insert; later ones lock the existing row and apply
// jobstatus.Enqueue to it.
func (s *Store) Enqueue(ctx context.Context, key jobstatus.Key) (jobstatus.Enqueued, error) {
	var res jobstatus.Enqueued
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`INSERT INTO document_job_status (document_id, job_type, user_id, status, attempt)
			 VALUES ($1, $2, $3, $4, 1)
			 ON CONFLICT (document_id, job_type) DO NOTHING`,
			key.DocumentID, string(key.JobType), key.UserID, string(jobstatus.StatusPending),
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			res = jobstatus.Enqueued{Attempt: 1, Started: true}
			return nil
		}

		cur, err := lockRecord(ctx, tx, key)
		if err != nil {
			return err
		}
		next, r := jobstatus.Enqueue(&cur, key)
		res = r
		if !r.Started {
			return nil
		}
		_, err = tx.Exec(ctx,
			`UPDATE document_job_status
			 SET status = $1, phase = NULL, attempt = $2, last_error = NULL,
			     enqueued_at = NOW(), updated_at = NOW()
			 WHERE document_id = $3 AND job_type = $4 AND user_id = $5`,
			string(next.Status), next.Attempt, key.DocumentID, string(key.JobType), key.UserID,
		)
		return err
	})
	if err != nil {
		return jobstatus.Enqueued{}, fmt.Errorf("jobstatus: enqueue %s for document %s: %w", key.JobType, key.DocumentID, err)
	}
	return res, nil
}

// MarkProcessing implements jobstatus.Writer.
func (s *Store) MarkProcessing(ctx context.Context, key jobstatus.Key, attempt int) error {
	return s.transition(ctx, key, attempt, jobstatus.StatusProcessing, "", "")
}

// SetPhase implements jobstatus.Writer.
func (s *Store) SetPhase(ctx context.Context, key jobstatus.Key, attempt int, phase string) error {
	return s.transition(ctx, key, attempt, jobstatus.StatusProcessing, phase, "")
}

// MarkSucceeded implements jobstatus.Writer.
func (s *Store) MarkSucceeded(ctx context.Context, key jobstatus.Key, attempt int) error {
	return s.transition(ctx, key, attempt, jobstatus.StatusSucceeded, "", "")
}

// MarkFailed implements jobstatus.Writer.
func (s *Store) MarkFailed(ctx context.Context, key jobstatus.Key, attempt int, reason string) error {
	return s.transition(ctx, key, attempt, jobstatus.StatusFailed, "", reason)
}

func (s *Store) transition(ctx context.Context, key jobstatus.Key, attempt int, to jobstatus.Status, phase, reason string) error {
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		cur, err := lockRecord(ctx, tx, key)
		if err != nil {
			return err
		}
		next, changed, err := jobstatus.Transition(cur, attempt, to, phase, reason)
		if err != nil || !changed {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE document_job_status
			 SET status = $1, phase = NULLIF($2, ''), last_error = NULLIF($3, ''), updated_at = NOW()
			 WHERE document_id = $4 AND job_type = $5 AND user_id = $6`,
			string(next.Status), next.Phase, next.LastError, key.DocumentID, string(key.JobType), key.UserID,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("jobstatus: mark %s %s for document %s: %w", key.JobType, to, key.DocumentID, err)
	}
	return nil
}

// lockRecord reads key's row FOR UPDATE, returning jobstatus.ErrNotFound
// when it doesn't exist or isn't visible to this tenant.
func lockRecord(ctx context.Context, tx pgx.Tx, key jobstatus.Key) (jobstatus.Record, error) {
	rec := jobstatus.Record{Key: key}
	var status string
	var phase, lastError *string
	err := tx.QueryRow(ctx,
		`SELECT status, phase, attempt, last_error
		 FROM document_job_status
		 WHERE document_id = $1 AND job_type = $2 AND user_id = $3
		 FOR UPDATE`,
		key.DocumentID, string(key.JobType), key.UserID,
	).Scan(&status, &phase, &rec.Attempt, &lastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return rec, jobstatus.ErrNotFound
	}
	if err != nil {
		return rec, err
	}
	rec.Status = jobstatus.Status(status)
	if phase != nil {
		rec.Phase = *phase
	}
	if lastError != nil {
		rec.LastError = *lastError
	}
	return rec, nil
}

// ActiveJobsForDocuments implements queue.JobStatusReader: every pending
// or processing job for userID's documents in documentIDs, in the order
// their current attempts were enqueued.
func (s *Store) ActiveJobsForDocuments(ctx context.Context, userID uuid.UUID, documentIDs []uuid.UUID) (map[uuid.UUID][]queue.JobStatus, error) {
	out := make(map[uuid.UUID][]queue.JobStatus, len(documentIDs))
	if len(documentIDs) == 0 {
		return out, nil
	}
	// []string with an explicit ::uuid[] cast, not []uuid.UUID: cmd/api's
	// pool uses pgx's simple protocol (PgBouncer transaction mode), which
	// has no encode plan for a bare []uuid.UUID. Same workaround as
	// internal/queue/pgstore.
	idStrings := make([]string, len(documentIDs))
	for i, id := range documentIDs {
		idStrings[i] = id.String()
	}
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT document_id, job_type, phase, status, last_error
			 FROM document_job_status
			 WHERE document_id = ANY($1::uuid[]) AND user_id = $2 AND status IN ('pending', 'processing')
			 ORDER BY document_id, enqueued_at`,
			idStrings, userID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var docID uuid.UUID
			var jobType, status string
			var phase, lastError *string
			if err := rows.Scan(&docID, &jobType, &phase, &status, &lastError); err != nil {
				return err
			}
			js := queue.JobStatus{Type: queue.JobType(jobType), Status: status}
			if phase != nil {
				js.Phase = *phase
			}
			if lastError != nil {
				js.LastError = *lastError
			}
			out[docID] = append(out[docID], js)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("jobstatus: active jobs for documents: %w", err)
	}
	return out, nil
}

var _ jobstatus.Store = (*Store)(nil)
