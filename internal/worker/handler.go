// Package worker holds the job handlers the stateless-jobs Lambda runs:
// edge extraction and canonicalization. internal/worker/lambda dispatches
// SQS messages to them. (The Batch-backed job types run as Step Functions
// state machines instead; see internal/pipeline.)
package worker

import (
	"context"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Handler processes one job of a single job type.
type Handler interface {
	// Handle runs the job. A non-nil error fails it; mark errors worth
	// retrying with pipeline.Transient (see retryable).
	Handle(ctx context.Context, job *queue.Job) error
	// OnFailed is called once the job has failed permanently.
	OnFailed(ctx context.Context, job *queue.Job)
}
