package lambda_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/queue"
	worklambda "github.com/kunalpednekar/dumpster/internal/worker/lambda"
)

// fakeHandler is a minimal worker.Handler test double: it records every
// job it was asked to Handle, and returns handleErr (if set) from every
// call.
type fakeHandler struct {
	mu          sync.Mutex
	handleErr   error
	handledJobs []*queue.Job
	failedJobs  []*queue.Job
}

func (h *fakeHandler) Handle(_ context.Context, job *queue.Job) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handledJobs = append(h.handledJobs, job)
	return h.handleErr
}

func (h *fakeHandler) OnFailed(_ context.Context, job *queue.Job) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failedJobs = append(h.failedJobs, job)
}

// sqsRecord builds an events.SQSMessage carrying the same JSON envelope
// internal/queue/sqs.Store publishes.
func sqsRecord(t *testing.T, messageID string, jobType queue.JobType, docID, userID uuid.UUID) events.SQSMessage {
	t.Helper()
	body, err := json.Marshal(struct {
		Type       queue.JobType `json:"type"`
		DocumentID uuid.UUID     `json:"document_id"`
		UserID     uuid.UUID     `json:"user_id"`
	}{Type: jobType, DocumentID: docID, UserID: userID})
	if err != nil {
		t.Fatalf("marshal test message: %v", err)
	}
	return events.SQSMessage{MessageId: messageID, Body: string(body)}
}

func TestDispatcher_HandleSQSEvent_DispatchesByJobType(t *testing.T) {
	edgeHandler := &fakeHandler{}
	canonHandler := &fakeHandler{}
	d := worklambda.NewDispatcher().
		RegisterHandler(queue.JobTypeEdgeExtraction, edgeHandler).
		RegisterHandler(queue.JobTypeCanonicalization, canonHandler)

	docID, userID := uuid.New(), uuid.New()
	event := events.SQSEvent{Records: []events.SQSMessage{
		sqsRecord(t, "msg-1", queue.JobTypeEdgeExtraction, docID, userID),
	}}

	resp, err := d.HandleSQSEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("HandleSQSEvent: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("BatchItemFailures: got %v, want none", resp.BatchItemFailures)
	}
	if len(edgeHandler.handledJobs) != 1 {
		t.Fatalf("edge handler Handle calls: got %d, want 1", len(edgeHandler.handledJobs))
	}
	got := edgeHandler.handledJobs[0]
	if got.Type != queue.JobTypeEdgeExtraction || got.DocumentID != docID || got.UserID != userID {
		t.Errorf("dispatched job: got %+v, want Type=%s DocumentID=%s UserID=%s", got, queue.JobTypeEdgeExtraction, docID, userID)
	}
	if len(canonHandler.handledJobs) != 0 {
		t.Errorf("canonicalization handler should not have been called, got %d calls", len(canonHandler.handledJobs))
	}
}

func TestDispatcher_HandleSQSEvent_MultipleRecordsDispatchIndependently(t *testing.T) {
	edgeHandler := &fakeHandler{}
	canonHandler := &fakeHandler{}
	d := worklambda.NewDispatcher().
		RegisterHandler(queue.JobTypeEdgeExtraction, edgeHandler).
		RegisterHandler(queue.JobTypeCanonicalization, canonHandler)

	event := events.SQSEvent{Records: []events.SQSMessage{
		sqsRecord(t, "msg-1", queue.JobTypeEdgeExtraction, uuid.New(), uuid.New()),
		sqsRecord(t, "msg-2", queue.JobTypeCanonicalization, uuid.New(), uuid.New()),
		sqsRecord(t, "msg-3", queue.JobTypeEdgeExtraction, uuid.New(), uuid.New()),
	}}

	resp, err := d.HandleSQSEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("HandleSQSEvent: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("BatchItemFailures: got %v, want none", resp.BatchItemFailures)
	}
	if len(edgeHandler.handledJobs) != 2 {
		t.Errorf("edge handler Handle calls: got %d, want 2", len(edgeHandler.handledJobs))
	}
	if len(canonHandler.handledJobs) != 1 {
		t.Errorf("canonicalization handler Handle calls: got %d, want 1", len(canonHandler.handledJobs))
	}
}

func TestDispatcher_HandleSQSEvent_HandlerErrorReportsBatchItemFailure(t *testing.T) {
	failing := &fakeHandler{handleErr: errors.New("boom")}
	succeeding := &fakeHandler{}
	d := worklambda.NewDispatcher().
		RegisterHandler(queue.JobTypeEdgeExtraction, failing).
		RegisterHandler(queue.JobTypeCanonicalization, succeeding)

	event := events.SQSEvent{Records: []events.SQSMessage{
		sqsRecord(t, "msg-fail", queue.JobTypeEdgeExtraction, uuid.New(), uuid.New()),
		sqsRecord(t, "msg-ok", queue.JobTypeCanonicalization, uuid.New(), uuid.New()),
	}}

	resp, err := d.HandleSQSEvent(context.Background(), event)
	// The overall invocation must not itself error -- a per-record failure
	// is reported via BatchItemFailures, precisely so the successful
	// record ("msg-ok") isn't forced to redeliver alongside the failed one.
	if err != nil {
		t.Fatalf("HandleSQSEvent: %v, want nil (per-record failures use BatchItemFailures)", err)
	}
	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-fail" {
		t.Errorf("BatchItemFailures: got %v, want exactly [msg-fail]", resp.BatchItemFailures)
	}
	if len(succeeding.handledJobs) != 1 {
		t.Errorf("succeeding handler Handle calls: got %d, want 1 (must still run despite the other record's failure)", len(succeeding.handledJobs))
	}
}

func TestDispatcher_HandleSQSEvent_UnregisteredJobTypeReportsBatchItemFailure(t *testing.T) {
	d := worklambda.NewDispatcher() // no handlers registered at all

	event := events.SQSEvent{Records: []events.SQSMessage{
		sqsRecord(t, "msg-1", queue.JobTypeEntityExtraction, uuid.New(), uuid.New()),
	}}

	resp, err := d.HandleSQSEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("HandleSQSEvent: %v", err)
	}
	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-1" {
		t.Errorf("BatchItemFailures: got %v, want exactly [msg-1]", resp.BatchItemFailures)
	}
}

func TestDispatcher_HandleSQSEvent_MalformedBodyReportsBatchItemFailure(t *testing.T) {
	edgeHandler := &fakeHandler{}
	d := worklambda.NewDispatcher().RegisterHandler(queue.JobTypeEdgeExtraction, edgeHandler)

	event := events.SQSEvent{Records: []events.SQSMessage{
		{MessageId: "msg-bad", Body: "not json"},
	}}

	resp, err := d.HandleSQSEvent(context.Background(), event)
	if err != nil {
		t.Fatalf("HandleSQSEvent: %v", err)
	}
	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-bad" {
		t.Errorf("BatchItemFailures: got %v, want exactly [msg-bad]", resp.BatchItemFailures)
	}
	if len(edgeHandler.handledJobs) != 0 {
		t.Errorf("handler should not have been called for a malformed body, got %d calls", len(edgeHandler.handledJobs))
	}
}

func TestDispatcher_HandleSQSEvent_EmptyBatch(t *testing.T) {
	d := worklambda.NewDispatcher()
	resp, err := d.HandleSQSEvent(context.Background(), events.SQSEvent{})
	if err != nil {
		t.Fatalf("HandleSQSEvent: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("BatchItemFailures: got %v, want none", resp.BatchItemFailures)
	}
}
