package lambda_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	statusmem "github.com/kunalpednekar/dumpster/internal/jobstatus/memory"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/queue"
	worklambda "github.com/kunalpednekar/dumpster/internal/worker/lambda"
)

// stepFixture is one edge-extraction job, enqueued as attempt 1 the way
// the publisher records it, with a StepHandler that tracks its status.
type stepFixture struct {
	t       *testing.T
	ctx     context.Context
	status  *statusmem.Store
	handler *fakeHandler
	steps   *worklambda.StepHandler
	key     jobstatus.Key
}

func newStepFixture(t *testing.T) *stepFixture {
	t.Helper()
	f := &stepFixture{t: t, ctx: context.Background(), status: statusmem.New(), handler: &fakeHandler{}}
	f.steps = worklambda.NewStepHandler(f.status).Register(queue.JobTypeEdgeExtraction, f.handler)
	f.key = jobstatus.Key{UserID: uuid.New(), DocumentID: uuid.New(), JobType: queue.JobTypeEdgeExtraction}
	if _, err := f.status.Enqueue(f.ctx, f.key); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return f
}

// event is the payload a state machine Task sends: the step name and the
// execution input (plus, for record_failure, the Catch's $.error).
func (f *stepFixture) event(step string, attempt int, stepErr *pipeline.StepError) json.RawMessage {
	f.t.Helper()
	run := map[string]any{
		"type": f.key.JobType, "document_id": f.key.DocumentID, "user_id": f.key.UserID, "attempt": attempt,
	}
	if stepErr != nil {
		run["error"] = stepErr
	}
	raw, err := json.Marshal(map[string]any{"step": step, "run": run})
	if err != nil {
		f.t.Fatalf("marshal event: %v", err)
	}
	return raw
}

func (f *stepFixture) handle(step string, attempt int, stepErr *pipeline.StepError) error {
	f.t.Helper()
	_, err := f.steps.HandleStep(f.ctx, f.event(step, attempt, stepErr))
	return err
}

func (f *stepFixture) active() []queue.JobStatus {
	got, _ := f.status.ActiveJobsForDocuments(f.ctx, f.key.UserID, []uuid.UUID{f.key.DocumentID})
	return got[f.key.DocumentID]
}

// record returns the job's status record.
func (f *stepFixture) record() jobstatus.Record {
	f.t.Helper()
	rec, ok := f.status.Record(f.key)
	if !ok {
		f.t.Fatal("no status record for the job")
	}
	return rec
}

func TestRun_Success_MarksProcessingThenSucceeded(t *testing.T) {
	f := newStepFixture(t)
	var statusDuringRun []queue.JobStatus
	f.handler.onHandle = func() { statusDuringRun = f.active() }

	if err := f.handle("run", 1, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(f.handler.handledJobs) != 1 {
		t.Fatalf("handled %d jobs, want 1", len(f.handler.handledJobs))
	}
	if job := f.handler.handledJobs[0]; job.DocumentID != f.key.DocumentID || job.UserID != f.key.UserID || job.Type != f.key.JobType {
		t.Errorf("handled job = %+v, want the run's document, user and type", job)
	}
	if len(statusDuringRun) != 1 || statusDuringRun[0].Status != "processing" {
		t.Errorf("status while the handler ran = %+v, want processing", statusDuringRun)
	}
	if got := f.record().Status; got != jobstatus.StatusSucceeded {
		t.Errorf("status = %s, want succeeded", got)
	}
}

// The state machine's Retry matches TransientError by name, and
// aws-lambda-go names an error after its outermost type, so a transient
// handler error must come back as a top-level *pipeline.TransientError.
func TestRun_TransientFailure_ReturnsATransientErrorAndLeavesTheJobActive(t *testing.T) {
	f := newStepFixture(t)
	f.handler.handleErr = pipeline.Transient(errors.New("connection reset"))

	err := f.handle("run", 1, nil)
	if _, ok := err.(*pipeline.TransientError); !ok {
		t.Fatalf("err = %T(%v), want a top-level *pipeline.TransientError", err, err)
	}
	if len(f.active()) != 1 {
		t.Error("job is no longer active; a retried step must not have recorded a failure")
	}
}

// A deterministic failure is returned unmarked so the state machine
// doesn't retry it; its Catch then runs record_failure.
func TestRun_DeterministicFailure_ReturnsAnErrorTheStateMachineWontRetry(t *testing.T) {
	f := newStepFixture(t)
	f.handler.handleErr = errors.New("document has no chunks")

	err := f.handle("run", 1, nil)
	if err == nil {
		t.Fatal("run err = nil, want the handler's error")
	}
	if pipeline.IsTransient(err) {
		t.Errorf("err = %v is transient, want it unmarked so it isn't retried", err)
	}
	if !strings.Contains(err.Error(), "document has no chunks") {
		t.Errorf("err = %v, want it to carry the handler's message", err)
	}
	if len(f.handler.failedJobs) != 0 {
		t.Error("OnFailed ran during run; that's record_failure's job")
	}
}

func TestRun_SupersededAttempt_IsSkipped(t *testing.T) {
	f := newStepFixture(t)
	if err := f.status.MarkFailed(f.ctx, f.key, 1, "earlier failure"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if _, err := f.status.Enqueue(f.ctx, f.key); err != nil { // the user retried: attempt 2
		t.Fatalf("Enqueue: %v", err)
	}

	if err := f.handle("run", 1, nil); err != nil {
		t.Fatalf("run for a superseded attempt: %v, want nil", err)
	}
	if len(f.handler.handledJobs) != 0 {
		t.Error("handler ran for a superseded attempt")
	}
}

func TestRun_NoStatusRecord_RunsUntracked(t *testing.T) {
	f := newStepFixture(t)
	f.key.DocumentID = uuid.New() // a job the publisher never recorded

	if err := f.handle("run", 1, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(f.handler.handledJobs) != 1 {
		t.Errorf("handled %d jobs, want 1", len(f.handler.handledJobs))
	}
}

func TestRun_UnregisteredJobType_Fails(t *testing.T) {
	f := newStepFixture(t)
	f.key.JobType = queue.JobTypeCanonicalization

	if err := f.handle("run", 1, nil); err == nil {
		t.Error("run err = nil, want an error for a job type with no handler")
	}
}

func TestRecordFailure_MarksTheAttemptFailedWithTheCaughtErrorAndCallsOnFailed(t *testing.T) {
	f := newStepFixture(t)
	stepErr := &pipeline.StepError{Error: "TransientError", Cause: "connection reset"}

	if err := f.handle("record_failure", 1, stepErr); err != nil {
		t.Fatalf("record_failure: %v", err)
	}
	rec := f.record()
	if rec.Status != jobstatus.StatusFailed {
		t.Errorf("status = %s, want failed", rec.Status)
	}
	if rec.LastError != stepErr.Reason() {
		t.Errorf("last error = %q, want %q", rec.LastError, stepErr.Reason())
	}
	if len(f.handler.failedJobs) != 1 {
		t.Errorf("OnFailed calls = %d, want 1", len(f.handler.failedJobs))
	}
}

func TestRecordFailure_SupersededAttempt_IsANoOp(t *testing.T) {
	f := newStepFixture(t)
	if err := f.status.MarkProcessing(f.ctx, f.key, 1); err != nil {
		t.Fatalf("MarkProcessing: %v", err)
	}
	if err := f.status.MarkSucceeded(f.ctx, f.key, 1); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}

	if err := f.handle("record_failure", 1, &pipeline.StepError{Error: "States.TaskFailed", Cause: "late"}); err != nil {
		t.Fatalf("record_failure for a finished attempt: %v, want nil", err)
	}
	if got := f.record().Status; got != jobstatus.StatusSucceeded {
		t.Errorf("status = %s, want it left succeeded", got)
	}
}

func TestUnknownStep_Fails(t *testing.T) {
	f := newStepFixture(t)
	if err := f.handle("plan", 1, nil); err == nil {
		t.Error("err = nil, want an error for a step this Lambda doesn't run")
	}
}

func TestMalformedEvent_Fails(t *testing.T) {
	f := newStepFixture(t)
	if _, err := f.steps.HandleStep(f.ctx, json.RawMessage(`{"step":`)); err == nil {
		t.Error("err = nil, want a decode error")
	}
}
