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
package jobstatus

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Status is the lifecycle state of one job type for one document.
type Status string

// The four job states. Pending and processing are active; succeeded and
// failed are finished.
const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
)

// Active reports whether a job in this state is still in flight.
func (s Status) Active() bool { return false }

// Errors returned by Transition and by Writer implementations.
var (
	ErrNotFound          = errors.New("jobstatus: no status record for this job")
	ErrAttemptSuperseded = errors.New("jobstatus: attempt superseded by a newer one")
	ErrNotActive         = errors.New("jobstatus: job already finished")
	ErrInvalidTransition = errors.New("jobstatus: invalid transition")
)

// Key identifies one job type's status record for one document.
type Key struct {
	UserID     uuid.UUID
	DocumentID uuid.UUID
	JobType    queue.JobType
}

// Record is one job type's current status for one document.
type Record struct {
	Key
	Status    Status
	Phase     string
	Attempt   int
	LastError string
}

// Enqueued is the result of Writer.Enqueue.
type Enqueued struct {
	Attempt int
	Started bool
}

// Enqueue computes the record that results from enqueuing key.
func Enqueue(cur *Record, key Key) (Record, Enqueued) { return Record{}, Enqueued{} }

// Transition computes the record that results from moving cur to status to.
func Transition(cur Record, attempt int, to Status, phase, reason string) (Record, bool, error) {
	return cur, false, nil
}

// Writer records job progress.
type Writer interface {
	Enqueue(ctx context.Context, key Key) (Enqueued, error)
	MarkProcessing(ctx context.Context, key Key, attempt int) error
	SetPhase(ctx context.Context, key Key, attempt int, phase string) error
	MarkSucceeded(ctx context.Context, key Key, attempt int) error
	MarkFailed(ctx context.Context, key Key, attempt int, reason string) error
}

// Store is a Writer that can also serve the API's reads.
type Store interface {
	Writer
	queue.JobStatusReader
}
