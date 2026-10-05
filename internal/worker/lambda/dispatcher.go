// Package lambda adapts worker.Handler dispatch to AWS Lambda's SQS event
// shape — the vendor-specific seam that keeps internal/worker itself
// Lambda-agnostic, matching this project's "no vendor SDK imports outside
// a dedicated adapter package" convention (see internal/llm/awsbatch,
// internal/entity/awsbatch for the same pattern applied to AWS Batch).
//
// This package's only job is translating between an SQS event and the
// existing, already-tested Handler interface — the handlers it dispatches
// to (EdgeHandler, CanonicalizationHandler, ...) are unmodified and run
// identically whether invoked from here or from the ECS worker's
// Consumer.Dequeue loop.
package lambda

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"

	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"

	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

// message mirrors the JSON envelope internal/queue/sqs.Store publishes.
// Deliberately its own type rather than importing queue/sqs: that would
// pull a publish-side package into a consume-side one, for a three-field
// struct neither package should have to version together.
type message struct {
	Type       queue.JobType `json:"type"`
	DocumentID uuid.UUID     `json:"document_id"`
	UserID     uuid.UUID     `json:"user_id"`
	// Attempt is the job's attempt number in the status store; 0 for a
	// message published before attempts existed, which runs untracked.
	Attempt int `json:"attempt"`
}

// StatusWriter is the part of jobstatus.Writer the dispatcher uses.
type StatusWriter interface {
	MarkProcessing(ctx context.Context, key jobstatus.Key, attempt int) error
	MarkSucceeded(ctx context.Context, key jobstatus.Key, attempt int) error
	MarkFailed(ctx context.Context, key jobstatus.Key, attempt int, reason string) error
}

// Dispatcher routes each SQS record to the worker.Handler registered for
// its JobType.
type Dispatcher struct {
	handlers        map[queue.JobType]worker.Handler
	status          StatusWriter
	maxReceiveCount int
}

// maxReasonLen caps the failure reason stored in the status record.
const maxReasonLen = 2000

// WithStatus makes the dispatcher record each job's progress in status
// (see HandleSQSEvent). maxReceiveCount must match the queues' redrive
// policy (terraform/modules/pipeline/queues.tf), so the dispatcher knows which delivery
// is the last one SQS will make.
func (d *Dispatcher) WithStatus(status StatusWriter, maxReceiveCount int) *Dispatcher {
	d.status = status
	d.maxReceiveCount = maxReceiveCount
	return d
}

// NewDispatcher returns a Dispatcher with no handlers registered; use
// RegisterHandler to add one per queue.JobType this Lambda should process.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{handlers: make(map[queue.JobType]worker.Handler)}
}

// RegisterHandler wires handler to process jobs of the given type,
// returning the Dispatcher for chaining (matching worker.New's callers'
// existing WithX-style wiring idiom).
func (d *Dispatcher) RegisterHandler(jobType queue.JobType, handler worker.Handler) *Dispatcher {
	d.handlers[jobType] = handler
	return d
}

// HandleSQSEvent processes every record in event, dispatching to the
// registered Handler for that record's job type. A record's failure
// (a malformed body, an unregistered job type, or the handler itself
// returning an error) is reported via BatchItemFailures — SQS's
// partial-batch-failure mechanism (requires the event source mapping's
// ReportBatchItemFailures function response type) — rather than failing
// the whole invocation, so only the records that actually failed go back
// on the queue for redelivery, not the ones that already succeeded.
//
// With a status store (WithStatus) and a message carrying an attempt, each
// job's progress is recorded the same way the state machines record theirs:
//   - processing before the handler runs, succeeded after;
//   - a message for an attempt the user has since retried is acknowledged
//     without running;
//   - a failure is redelivered only if it's transient (pipeline.Transient)
//     and SQS has deliveries left. Otherwise it's recorded as failed (the
//     UI's retry button acts on that), OnFailed runs, and the message is
//     acknowledged, so a failure that can't improve isn't retried.
//   - A failed status write is redelivered, so it gets recorded.
//
// The dead-letter queue remains the backstop for messages that never reach
// a handler (a malformed body, an unregistered job type) and for messages
// without an attempt.
func (d *Dispatcher) HandleSQSEvent(ctx context.Context, event events.SQSEvent) (events.SQSEventResponse, error) {
	var resp events.SQSEventResponse
	for _, record := range event.Records {
		if err := d.handleRecord(ctx, record); err != nil {
			resp.BatchItemFailures = append(resp.BatchItemFailures, events.SQSBatchItemFailure{
				ItemIdentifier: record.MessageId,
			})
		}
	}
	return resp, nil
}

func (d *Dispatcher) handleRecord(ctx context.Context, record events.SQSMessage) error {
	var msg message
	if err := json.Unmarshal([]byte(record.Body), &msg); err != nil {
		return fmt.Errorf("worker/lambda: unmarshal message %s: %w", record.MessageId, err)
	}

	handler, ok := d.handlers[msg.Type]
	if !ok {
		return fmt.Errorf("worker/lambda: no handler registered for job type %q (message %s)", msg.Type, record.MessageId)
	}

	ctx = auth.WithUserID(ctx, msg.UserID)
	key := jobstatus.Key{UserID: msg.UserID, DocumentID: msg.DocumentID, JobType: msg.Type}
	tracked := d.status != nil && msg.Attempt > 0
	if tracked {
		err := d.status.MarkProcessing(ctx, key, msg.Attempt)
		switch {
		case superseded(err):
			log.Printf("worker/lambda: skipping %s for document %s: attempt %d is no longer current", msg.Type, msg.DocumentID, msg.Attempt)
			return nil
		case errors.Is(err, jobstatus.ErrNotFound):
			tracked = false // the publisher never recorded this job; run it untracked
		case err != nil:
			return fmt.Errorf("worker/lambda: mark %s processing for document %s: %w", msg.Type, msg.DocumentID, err)
		}
	}

	job := &queue.Job{Type: msg.Type, DocumentID: msg.DocumentID, UserID: msg.UserID}
	handleErr := handler.Handle(ctx, job)
	if handleErr == nil {
		if tracked {
			if err := d.status.MarkSucceeded(ctx, key, msg.Attempt); err != nil && !superseded(err) {
				return fmt.Errorf("worker/lambda: mark %s succeeded for document %s: %w", msg.Type, msg.DocumentID, err)
			}
		}
		return nil
	}

	handleErr = fmt.Errorf("worker/lambda: handle %s job for document %s: %w", msg.Type, msg.DocumentID, handleErr)
	if !tracked || (pipeline.IsTransient(handleErr) && receiveCount(record) < d.maxReceiveCount) {
		return handleErr
	}

	reason := handleErr.Error()
	if len(reason) > maxReasonLen {
		reason = reason[:maxReasonLen]
	}
	if err := d.status.MarkFailed(ctx, key, msg.Attempt, reason); err != nil && !superseded(err) {
		return errors.Join(handleErr, fmt.Errorf("worker/lambda: record failure: %w", err))
	}
	handler.OnFailed(ctx, job)
	log.Printf("worker/lambda: %v (attempt %d failed permanently)", handleErr, msg.Attempt)
	return nil
}

// superseded reports whether err means the attempt is no longer current or
// has already finished.
func superseded(err error) bool {
	return errors.Is(err, jobstatus.ErrAttemptSuperseded) || errors.Is(err, jobstatus.ErrNotActive)
}

// receiveCount is how many times SQS has delivered record, counting this
// delivery. It defaults to 1 if the attribute is missing.
func receiveCount(record events.SQSMessage) int {
	n, err := strconv.Atoi(record.Attributes["ApproximateReceiveCount"])
	if err != nil || n < 1 {
		return 1
	}
	return n
}
