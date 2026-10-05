// Package jobstatus tracks the state of each document's background
// pipeline jobs: whether each job type is pending, processing, succeeded or
// failed, its current phase, and its attempt number. It replaces the jobs
// table as the source the upload progress UI and the delete/retry guards
// read from once work runs on SQS + Lambda and Step Functions instead of
// the Postgres-polling worker.
//
// Writers are whatever starts or runs a job: the publisher records a new
// attempt, and the Lambdas and state machines' finalize and Catch steps
// record progress, success and failure. cmd/api only reads, through
// queue.JobStatusReader.
//
// The state rules live in Enqueue and Transition as pure functions, so
// every Store implementation applies exactly the same rules and they are
// unit-tested once, without a database.
package jobstatus

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Status is the lifecycle state of one job type for one document.
type Status string

// The four job states. Pending and processing are active; succeeded and
// failed are finished. A failed record is how a permanently failed job
// surfaces to the UI and its retry button: there is no separate
// dead-letter store for the Step Functions-backed job types.
const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
)

// Active reports whether a job in this state is still in flight. Only
// active jobs appear in queue.JobStatusReader results and block a
// document's delete/retry.
func (s Status) Active() bool {
	return s == StatusPending || s == StatusProcessing
}

// Errors returned by Transition and by Writer implementations.
var (
	// ErrNotFound means no status record exists for the key, or it
	// belongs to another tenant.
	ErrNotFound = errors.New("jobstatus: no status record for this job")
	// ErrAttemptSuperseded means the caller is reporting on an attempt
	// that is no longer the current one, e.g. a slow execution finishing
	// after the user already retried. The current attempt is left alone.
	ErrAttemptSuperseded = errors.New("jobstatus: attempt superseded by a newer one")
	// ErrNotActive means the job already finished with a different
	// outcome than the one being recorded.
	ErrNotActive = errors.New("jobstatus: job already finished")
	// ErrInvalidTransition means the target status can't be reached via
	// Transition. A job only becomes pending again through Enqueue.
	ErrInvalidTransition = errors.New("jobstatus: invalid transition")
)

// Key identifies one job type's status record for one document. UserID is
// part of the key so every read and write is tenant-scoped.
type Key struct {
	UserID     uuid.UUID
	DocumentID uuid.UUID
	JobType    queue.JobType
}

// Record is one job type's current status for one document.
type Record struct {
	Key
	Status Status
	// Phase is an optional sub-stage within the job type (see
	// queue.PhaseEmbedding); empty when the job type has none.
	Phase string
	// Attempt starts at 1 and increments each time a finished job is
	// enqueued again. It is part of the Step Functions execution name
	// ({job_type}-{document_id}-{attempt}), since a name can't be reused
	// for 90 days.
	Attempt   int
	LastError string
}

// Enqueued is the result of Writer.Enqueue.
type Enqueued struct {
	// Attempt is the current attempt number after the call.
	Attempt int
	// Started is false when the job was already active, in which case
	// the caller must not start another run.
	Started bool
}

// Enqueue computes the record that results from enqueuing key, given its
// current record (nil if none exists yet).
//
//   - No record: attempt 1 starts as pending.
//   - An active record: nothing changes, and the result reports
//     Started=false so a duplicate publish doesn't start a second run.
//   - A finished record: the next attempt starts as pending, with the
//     previous attempt's phase and error cleared.
func Enqueue(cur *Record, key Key) (Record, Enqueued) {
	if cur == nil {
		return Record{Key: key, Status: StatusPending, Attempt: 1}, Enqueued{Attempt: 1, Started: true}
	}
	if cur.Status.Active() {
		return *cur, Enqueued{Attempt: cur.Attempt, Started: false}
	}
	next := Record{Key: cur.Key, Status: StatusPending, Attempt: cur.Attempt + 1}
	return next, Enqueued{Attempt: next.Attempt, Started: true}
}

// Transition computes the record that results from moving cur to status
// to on behalf of attempt. changed reports whether anything differs from
// cur, so stores can skip a no-op write.
//
//   - attempt must be cur's current attempt (ErrAttemptSuperseded).
//   - processing: allowed from an active record. A non-empty phase
//     replaces the current one; an empty phase keeps it.
//   - succeeded: allowed from an active record, clearing phase and error.
//     Repeating it is a no-op, so a redelivered finalize step is safe.
//   - failed: allowed from an active record, recording reason and keeping
//     the phase for diagnosis. Repeating it is a no-op that keeps the
//     first reason.
//   - Any other move out of a finished record returns ErrNotActive, and
//     pending is never a valid target (ErrInvalidTransition).
//
// On error the returned record is cur, unchanged.
func Transition(cur Record, attempt int, to Status, phase, reason string) (next Record, changed bool, err error) {
	if attempt != cur.Attempt {
		return cur, false, fmt.Errorf("%w: attempt %d, current %d", ErrAttemptSuperseded, attempt, cur.Attempt)
	}
	switch to {
	case StatusProcessing, StatusSucceeded, StatusFailed:
	default:
		return cur, false, fmt.Errorf("%w: to %q", ErrInvalidTransition, to)
	}
	if !cur.Status.Active() {
		if cur.Status == to {
			return cur, false, nil
		}
		return cur, false, fmt.Errorf("%w: %s, cannot move to %s", ErrNotActive, cur.Status, to)
	}

	next = cur
	next.Status = to
	switch to {
	case StatusProcessing:
		if phase != "" {
			next.Phase = phase
		}
	case StatusSucceeded:
		next.Phase = ""
		next.LastError = ""
	case StatusFailed:
		next.LastError = reason
	}
	return next, next != cur, nil
}

// Writer records job progress. Implementations must be safe for
// concurrent use and must apply Enqueue and Transition atomically per key.
// Every method is scoped to key.UserID.
type Writer interface {
	// Enqueue records a new attempt for key, unless the job is already
	// active (see the package-level Enqueue for the rules). Callers start
	// a run only when the result's Started is true, using its Attempt.
	Enqueue(ctx context.Context, key Key) (Enqueued, error)
	// MarkProcessing records that attempt has started running.
	MarkProcessing(ctx context.Context, key Key, attempt int) error
	// SetPhase records attempt's current sub-stage (and marks it
	// processing if it wasn't already).
	SetPhase(ctx context.Context, key Key, attempt int, phase string) error
	// MarkSucceeded records that attempt finished successfully.
	MarkSucceeded(ctx context.Context, key Key, attempt int) error
	// MarkFailed records that attempt failed permanently with reason.
	MarkFailed(ctx context.Context, key Key, attempt int, reason string) error
}

// Store is a Writer that can also serve the API's reads.
type Store interface {
	Writer
	queue.JobStatusReader
}
