package lambda_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	statusmem "github.com/kunalpednekar/dumpster/internal/jobstatus/memory"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/queue"
	worklambda "github.com/kunalpednekar/dumpster/internal/worker/lambda"
)

// maxReceives matches the redrive policy's maxReceiveCount for both
// Lambda queues (terraform/queue_sqs.tf).
const maxReceives = 3

// statusFixture is one edge-extraction job, enqueued as attempt 1 the way
// the publisher records it, with a dispatcher that tracks its status.
type statusFixture struct {
	t          *testing.T
	ctx        context.Context
	status     *statusmem.Store
	handler    *fakeHandler
	dispatcher *worklambda.Dispatcher
	key        jobstatus.Key
}

func newStatusFixture(t *testing.T) *statusFixture {
	t.Helper()
	f := &statusFixture{t: t, ctx: context.Background(), status: statusmem.New(), handler: &fakeHandler{}}
	f.dispatcher = worklambda.NewDispatcher().
		RegisterHandler(queue.JobTypeEdgeExtraction, f.handler).
		WithStatus(f.status, maxReceives)
	f.key = jobstatus.Key{UserID: uuid.New(), DocumentID: uuid.New(), JobType: queue.JobTypeEdgeExtraction}
	if _, err := f.status.Enqueue(f.ctx, f.key); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return f
}

// record is an SQS message for f's job at attempt, on its receiveCount-th
// delivery.
func (f *statusFixture) record(attempt, receiveCount int) events.SQSMessage {
	body, _ := json.Marshal(map[string]any{
		"type": f.key.JobType, "document_id": f.key.DocumentID, "user_id": f.key.UserID, "attempt": attempt,
	})
	return events.SQSMessage{
		MessageId:  "m-" + strconv.Itoa(receiveCount),
		Body:       string(body),
		Attributes: map[string]string{"ApproximateReceiveCount": strconv.Itoa(receiveCount)},
	}
}

// deliver runs one delivery and reports whether SQS will redeliver it.
func (f *statusFixture) deliver(attempt, receiveCount int) (redelivered bool) {
	f.t.Helper()
	resp, err := f.dispatcher.HandleSQSEvent(f.ctx, events.SQSEvent{Records: []events.SQSMessage{f.record(attempt, receiveCount)}})
	if err != nil {
		f.t.Fatalf("HandleSQSEvent: %v", err)
	}
	return len(resp.BatchItemFailures) == 1
}

func (f *statusFixture) active() []queue.JobStatus {
	got, _ := f.status.ActiveJobsForDocuments(f.ctx, f.key.UserID, []uuid.UUID{f.key.DocumentID})
	return got[f.key.DocumentID]
}

// finished reports whether the attempt has finished, and if so whether it
// failed (re-enqueueing starts attempt 2 either way; a succeeded job
// rejects a late MarkFailed with ErrNotActive, a failed one accepts it as
// a no-op).
func (f *statusFixture) outcome() string {
	if len(f.active()) != 0 {
		return "active"
	}
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "probe"); errors.Is(err, jobstatus.ErrNotActive) {
		return "succeeded"
	}
	return "failed"
}

func TestStatus_Success_MarksProcessingThenSucceeded(t *testing.T) {
	f := newStatusFixture(t)
	var statusDuringRun []queue.JobStatus
	f.handler.onHandle = func() { statusDuringRun = f.active() }

	if f.deliver(1, 1) {
		t.Fatal("a successful job was redelivered")
	}
	if len(statusDuringRun) != 1 || statusDuringRun[0].Status != "processing" {
		t.Errorf("status while the handler ran = %+v, want processing", statusDuringRun)
	}
	if got := f.outcome(); got != "succeeded" {
		t.Errorf("outcome = %s, want succeeded", got)
	}
}

func TestStatus_TransientFailureBeforeTheLastDelivery_IsRedelivered(t *testing.T) {
	f := newStatusFixture(t)
	f.handler.handleErr = pipeline.Transient(errors.New("connection reset"))

	if !f.deliver(1, 1) {
		t.Error("a transient failure on delivery 1 of 3 should be redelivered")
	}
	if got := f.outcome(); got != "active" {
		t.Errorf("outcome = %s, want still active while retries remain", got)
	}
}

func TestStatus_TransientFailureThenSuccess(t *testing.T) {
	f := newStatusFixture(t)
	f.handler.handleErr = pipeline.Transient(errors.New("connection reset"))
	f.deliver(1, 1)
	f.handler.handleErr = nil

	if f.deliver(1, 2) {
		t.Error("the successful retry was redelivered")
	}
	if got := f.outcome(); got != "succeeded" {
		t.Errorf("outcome = %s, want succeeded", got)
	}
}

func TestStatus_TransientFailureOnTheLastDelivery_RecordsTheFailure(t *testing.T) {
	f := newStatusFixture(t)
	f.handler.handleErr = pipeline.Transient(errors.New("connection reset"))

	if f.deliver(1, maxReceives) {
		t.Error("the last allowed delivery should not be redelivered")
	}
	if got := f.outcome(); got != "failed" {
		t.Errorf("outcome = %s, want failed", got)
	}
	if len(f.handler.failedJobs) != 1 {
		t.Errorf("OnFailed calls = %d, want 1", len(f.handler.failedJobs))
	}
}

func TestStatus_DeterministicFailure_RecordsTheFailureWithoutRetrying(t *testing.T) {
	f := newStatusFixture(t)
	f.handler.handleErr = errors.New("document not found")

	if f.deliver(1, 1) {
		t.Error("a deterministic failure should not be redelivered")
	}
	if got := f.outcome(); got != "failed" {
		t.Errorf("outcome = %s, want failed", got)
	}
	res, err := f.status.Enqueue(f.ctx, f.key)
	if err != nil || !res.Started || res.Attempt != 2 {
		t.Errorf("retry Enqueue = %+v, %v; want attempt 2 (the retry button works)", res, err)
	}
}

func TestStatus_SupersededAttempt_IsSkippedAndAcknowledged(t *testing.T) {
	f := newStatusFixture(t)
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "boom"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if _, err := f.status.Enqueue(f.ctx, f.key); err != nil {
		t.Fatalf("re-Enqueue: %v", err)
	}

	if f.deliver(1, 1) {
		t.Error("a message for a superseded attempt should be acknowledged, not redelivered")
	}
	if len(f.handler.handledJobs) != 0 {
		t.Error("the handler ran for a superseded attempt")
	}
	if a := f.active(); len(a) != 1 || a[0].Status != "pending" {
		t.Errorf("attempt 2 = %+v, want still pending", a)
	}
}

func TestStatus_NoStatusRecord_RunsUntracked(t *testing.T) {
	f := newStatusFixture(t)
	f.key.DocumentID = uuid.New() // a job the publisher never recorded

	if f.deliver(1, 1) {
		t.Error("an untracked successful job was redelivered")
	}
	if len(f.handler.handledJobs) != 1 {
		t.Errorf("handler calls = %d, want 1", len(f.handler.handledJobs))
	}
}

// A message without an attempt (published before attempts existed) keeps
// the old behavior: no status writes, and SQS redelivers any failure until
// the dead-letter queue takes it.
func TestStatus_MessageWithoutAttempt_KeepsTheOldBehavior(t *testing.T) {
	f := newStatusFixture(t)
	f.handler.handleErr = errors.New("document not found")

	if !f.deliver(0, 1) {
		t.Error("an untracked failure should be redelivered as before")
	}
	if a := f.active(); len(a) != 1 || a[0].Status != "pending" {
		t.Errorf("status = %+v, want untouched", a)
	}
}

// failingStatus fails the chosen write.
type failingStatus struct {
	jobstatus.Writer
	failProcessing, failSucceeded, failFailed error
}

func (s *failingStatus) MarkProcessing(ctx context.Context, k jobstatus.Key, a int) error {
	if s.failProcessing != nil {
		return s.failProcessing
	}
	return s.Writer.MarkProcessing(ctx, k, a)
}

func (s *failingStatus) MarkSucceeded(ctx context.Context, k jobstatus.Key, a int) error {
	if s.failSucceeded != nil {
		return s.failSucceeded
	}
	return s.Writer.MarkSucceeded(ctx, k, a)
}

func (s *failingStatus) MarkFailed(ctx context.Context, k jobstatus.Key, a int, r string) error {
	if s.failFailed != nil {
		return s.failFailed
	}
	return s.Writer.MarkFailed(ctx, k, a, r)
}

func TestStatus_StatusWriteFailures_AreRedelivered(t *testing.T) {
	dbDown := errors.New("database unavailable")
	cases := map[string]struct {
		status     failingStatus
		handlerErr error
		wantRun    bool
	}{
		"mark processing": {status: failingStatus{failProcessing: dbDown}, wantRun: false},
		"mark succeeded":  {status: failingStatus{failSucceeded: dbDown}, wantRun: true},
		"mark failed":     {status: failingStatus{failFailed: dbDown}, handlerErr: errors.New("bad"), wantRun: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newStatusFixture(t)
			tc.status.Writer = f.status
			f.handler.handleErr = tc.handlerErr
			f.dispatcher = worklambda.NewDispatcher().
				RegisterHandler(queue.JobTypeEdgeExtraction, f.handler).
				WithStatus(&tc.status, maxReceives)

			if !f.deliver(1, 1) {
				t.Error("a failed status write should be redelivered so it can be recorded")
			}
			if ran := len(f.handler.handledJobs) == 1; ran != tc.wantRun {
				t.Errorf("handler ran = %v, want %v", ran, tc.wantRun)
			}
		})
	}
}
