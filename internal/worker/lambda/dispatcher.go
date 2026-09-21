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
	"fmt"

	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"

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
}

// Dispatcher routes each SQS record to the worker.Handler registered for
// its JobType.
type Dispatcher struct {
	handlers map[queue.JobType]worker.Handler
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
// OnFailed is deliberately never called from here: the ECS worker calls
// it after Postgres Nack exhausts MaxAttempts, a decision made inside
// that same call. SQS's equivalent (a message exhausting its redrive
// policy and landing in the DLQ) happens entirely outside this Lambda's
// invocation, with no hook back into the code that ran — there is
// nothing here to call OnFailed from. See the "Event-Driven Job
// Orchestration" dev board for the known gap this leaves (low-stakes
// today: every handler registered by this package's current callers
// only logs in OnFailed, never touches persisted state).
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

	job := &queue.Job{Type: msg.Type, DocumentID: msg.DocumentID, UserID: msg.UserID}
	if err := handler.Handle(ctx, job); err != nil {
		return fmt.Errorf("worker/lambda: handle %s job for document %s: %w", msg.Type, msg.DocumentID, err)
	}
	return nil
}
