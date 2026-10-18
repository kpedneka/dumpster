package dispatch_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/jobstatus/memory"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/queue/dispatch"
	"github.com/kunalpednekar/dumpster/internal/queue/stepfunctions"
)

var (
	_ queue.Publisher       = (*dispatch.Publisher)(nil)
	_ dispatch.Target       = (*stepfunctions.Starter)(nil)
	_ dispatch.StatusWriter = (*memory.Store)(nil)
	_ dispatch.StatusWriter = jobstatus.Writer(nil)
)

// recordingTarget records every run it is handed, or fails with err.
type recordingTarget struct {
	mu   sync.Mutex
	err  error
	runs []queue.JobRun
}

func (r *recordingTarget) Dispatch(_ context.Context, run queue.JobRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.runs = append(r.runs, run)
	return nil
}

func (r *recordingTarget) Runs() []queue.JobRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]queue.JobRun(nil), r.runs...)
}

type fixture struct {
	status        *memory.Store
	target        *recordingTarget
	publisher     *dispatch.Publisher
	userID, docID uuid.UUID
}

func newFixture() *fixture {
	f := &fixture{status: memory.New(), target: &recordingTarget{}, userID: uuid.New(), docID: uuid.New()}
	f.publisher = dispatch.New(f.status, f.target)
	return f
}

func (f *fixture) active(t *testing.T) []queue.JobStatus {
	t.Helper()
	got, err := f.status.ActiveJobsForDocuments(context.Background(), f.userID, []uuid.UUID{f.docID})
	if err != nil {
		t.Fatalf("ActiveJobsForDocuments: %v", err)
	}
	return got[f.docID]
}

func TestPublish_DispatchesEveryJobTypeToTheTargetAndRecordsAttemptOne(t *testing.T) {
	cases := []struct {
		name    string
		publish func(f *fixture) error
		jobType queue.JobType
	}{
		{"document uploaded", func(f *fixture) error {
			return f.publisher.PublishDocumentUploaded(context.Background(), queue.DocumentUploaded{DocumentID: f.docID, UserID: f.userID})
		}, queue.JobTypeDocumentIndexing},
		{"entity extraction", func(f *fixture) error {
			return f.publisher.PublishEntityExtraction(context.Background(), queue.EntityExtractionRequested{DocumentID: f.docID, UserID: f.userID})
		}, queue.JobTypeEntityExtraction},
		{"region classification", func(f *fixture) error {
			return f.publisher.PublishRegionClassification(context.Background(), queue.RegionClassificationRequested{DocumentID: f.docID, UserID: f.userID})
		}, queue.JobTypeRegionClassification},
		{"edge extraction", func(f *fixture) error {
			return f.publisher.PublishEdgeExtraction(context.Background(), queue.EdgeExtractionRequested{DocumentID: f.docID, UserID: f.userID})
		}, queue.JobTypeEdgeExtraction},
		{"canonicalization", func(f *fixture) error {
			return f.publisher.PublishCanonicalization(context.Background(), queue.CanonicalizationRequested{DocumentID: f.docID, UserID: f.userID})
		}, queue.JobTypeCanonicalization},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			if err := tc.publish(f); err != nil {
				t.Fatalf("publish: %v", err)
			}

			want := queue.JobRun{Type: tc.jobType, DocumentID: f.docID, UserID: f.userID, Attempt: 1}
			if runs := f.target.Runs(); len(runs) != 1 || runs[0] != want {
				t.Errorf("runs = %+v, want [%+v]", runs, want)
			}
			if active := f.active(t); len(active) != 1 || active[0].Type != tc.jobType || active[0].Status != "pending" {
				t.Errorf("status = %+v, want one pending %s job", active, tc.jobType)
			}
		})
	}
}

func TestPublish_WhileActive_DoesNotDispatchAgain(t *testing.T) {
	f := newFixture()
	evt := queue.DocumentUploaded{DocumentID: f.docID, UserID: f.userID}
	for i := 0; i < 2; i++ {
		if err := f.publisher.PublishDocumentUploaded(context.Background(), evt); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
	if runs := f.target.Runs(); len(runs) != 1 {
		t.Errorf("runs = %+v, want exactly one", runs)
	}
}

func TestPublish_AfterFinished_DispatchesTheNextAttempt(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	evt := queue.EntityExtractionRequested{DocumentID: f.docID, UserID: f.userID}

	if err := f.publisher.PublishEntityExtraction(ctx, evt); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	key := jobstatus.Key{UserID: f.userID, DocumentID: f.docID, JobType: queue.JobTypeEntityExtraction}
	if err := f.status.MarkSucceeded(ctx, key, 1); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}
	if err := f.publisher.PublishEntityExtraction(ctx, evt); err != nil {
		t.Fatalf("second publish: %v", err)
	}

	runs := f.target.Runs()
	if len(runs) != 2 || runs[1].Attempt != 2 {
		t.Errorf("runs = %+v, want a second run with Attempt 2", runs)
	}
}

func TestPublish_DispatchFailure_MarksTheAttemptFailedSoItCanBeRetried(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	boom := errors.New("start execution: throttled")
	f.target.err = boom
	evt := queue.RegionClassificationRequested{DocumentID: f.docID, UserID: f.userID}

	if err := f.publisher.PublishRegionClassification(ctx, evt); !errors.Is(err, boom) {
		t.Fatalf("publish err = %v, want it to wrap %v", err, boom)
	}
	if active := f.active(t); len(active) != 0 {
		t.Fatalf("status = %+v, want the failed attempt to no longer be active", active)
	}

	f.target.err = nil
	if err := f.publisher.PublishRegionClassification(ctx, evt); err != nil {
		t.Fatalf("retry publish: %v", err)
	}
	if runs := f.target.Runs(); len(runs) != 1 || runs[0].Attempt != 2 {
		t.Errorf("runs = %+v, want one run with Attempt 2", runs)
	}
}

// failingStatus is a StatusWriter whose calls fail on demand.
type failingStatus struct {
	enqueueErr, markFailedErr error
}

func (s *failingStatus) Enqueue(context.Context, jobstatus.Key) (jobstatus.Enqueued, error) {
	if s.enqueueErr != nil {
		return jobstatus.Enqueued{}, s.enqueueErr
	}
	return jobstatus.Enqueued{Attempt: 1, Started: true}, nil
}

func (s *failingStatus) MarkFailed(context.Context, jobstatus.Key, int, string) error {
	return s.markFailedErr
}

func TestPublish_EnqueueFailure_IsReturnedWithoutDispatching(t *testing.T) {
	dbDown := errors.New("database unavailable")
	target := &recordingTarget{}
	p := dispatch.New(&failingStatus{enqueueErr: dbDown}, target)

	err := p.PublishEdgeExtraction(context.Background(), queue.EdgeExtractionRequested{DocumentID: uuid.New(), UserID: uuid.New()})
	if !errors.Is(err, dbDown) {
		t.Errorf("err = %v, want it to wrap %v", err, dbDown)
	}
	if n := len(target.Runs()); n != 0 {
		t.Errorf("dispatched %d runs, want none", n)
	}
}

func TestPublish_DispatchAndMarkFailedBothFail_ReturnsBothErrors(t *testing.T) {
	sendErr, markErr := errors.New("step functions unavailable"), errors.New("database unavailable")
	p := dispatch.New(&failingStatus{markFailedErr: markErr}, &recordingTarget{err: sendErr})

	err := p.PublishCanonicalization(context.Background(), queue.CanonicalizationRequested{DocumentID: uuid.New(), UserID: uuid.New()})
	if !errors.Is(err, sendErr) || !errors.Is(err, markErr) {
		t.Errorf("err = %v, want it to wrap both %v and %v", err, sendErr, markErr)
	}
}
