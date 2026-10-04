package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/jobstatus/memory"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

var _ jobstatus.Store = (*memory.Store)(nil)

func key(userID, docID uuid.UUID, jobType queue.JobType) jobstatus.Key {
	return jobstatus.Key{UserID: userID, DocumentID: docID, JobType: jobType}
}

func activeFor(t *testing.T, s *memory.Store, userID, docID uuid.UUID) []queue.JobStatus {
	t.Helper()
	got, err := s.ActiveJobsForDocuments(context.Background(), userID, []uuid.UUID{docID})
	if err != nil {
		t.Fatalf("ActiveJobsForDocuments: %v", err)
	}
	return got[docID]
}

func TestEnqueue_NewJobIsActiveAndPending(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	userID, docID := uuid.New(), uuid.New()

	res, err := s.Enqueue(ctx, key(userID, docID, queue.JobTypeDocumentIndexing))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if !res.Started || res.Attempt != 1 {
		t.Errorf("Enqueue result = %+v, want {Attempt:1 Started:true}", res)
	}

	active := activeFor(t, s, userID, docID)
	want := []queue.JobStatus{{Type: queue.JobTypeDocumentIndexing, Status: "pending"}}
	if len(active) != 1 || active[0] != want[0] {
		t.Errorf("active = %+v, want %+v", active, want)
	}
}

func TestEnqueue_WhileActive_DoesNotStartAnotherAttempt(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	k := key(uuid.New(), uuid.New(), queue.JobTypeEntityExtraction)

	if _, err := s.Enqueue(ctx, k); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	res, err := s.Enqueue(ctx, k)
	if err != nil {
		t.Fatalf("second Enqueue: %v", err)
	}
	if res.Started || res.Attempt != 1 {
		t.Errorf("second Enqueue result = %+v, want {Attempt:1 Started:false}", res)
	}
}

func TestLifecycle_ProcessingPhaseSucceededThenRetry(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	userID, docID := uuid.New(), uuid.New()
	k := key(userID, docID, queue.JobTypeRegionClassification)

	if _, err := s.Enqueue(ctx, k); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := s.MarkProcessing(ctx, k, 1); err != nil {
		t.Fatalf("MarkProcessing: %v", err)
	}
	if err := s.SetPhase(ctx, k, 1, queue.PhaseEmbedding); err != nil {
		t.Fatalf("SetPhase: %v", err)
	}
	active := activeFor(t, s, userID, docID)
	if len(active) != 1 || active[0].Status != "processing" || active[0].Phase != queue.PhaseEmbedding {
		t.Fatalf("after SetPhase: active = %+v, want one processing job in phase %q", active, queue.PhaseEmbedding)
	}

	if err := s.MarkSucceeded(ctx, k, 1); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}
	if active := activeFor(t, s, userID, docID); len(active) != 0 {
		t.Fatalf("after MarkSucceeded: active = %+v, want none", active)
	}

	res, err := s.Enqueue(ctx, k)
	if err != nil {
		t.Fatalf("re-Enqueue: %v", err)
	}
	if !res.Started || res.Attempt != 2 {
		t.Errorf("re-Enqueue result = %+v, want {Attempt:2 Started:true}", res)
	}
}

func TestMarkFailed_RemovesFromActiveAndAllowsRetry(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	userID, docID := uuid.New(), uuid.New()
	k := key(userID, docID, queue.JobTypeEntityExtraction)

	if _, err := s.Enqueue(ctx, k); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := s.MarkFailed(ctx, k, 1, "unprocessable document"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if active := activeFor(t, s, userID, docID); len(active) != 0 {
		t.Fatalf("after MarkFailed: active = %+v, want none", active)
	}

	res, err := s.Enqueue(ctx, k)
	if err != nil {
		t.Fatalf("re-Enqueue: %v", err)
	}
	if !res.Started || res.Attempt != 2 {
		t.Errorf("re-Enqueue result = %+v, want {Attempt:2 Started:true}", res)
	}
}

func TestStaleAttempt_IsRejectedAndLeavesNewerAttemptAlone(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	userID, docID := uuid.New(), uuid.New()
	k := key(userID, docID, queue.JobTypeDocumentIndexing)

	if _, err := s.Enqueue(ctx, k); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := s.MarkFailed(ctx, k, 1, "boom"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if _, err := s.Enqueue(ctx, k); err != nil {
		t.Fatalf("re-Enqueue: %v", err)
	}

	if err := s.MarkSucceeded(ctx, k, 1); !errors.Is(err, jobstatus.ErrAttemptSuperseded) {
		t.Fatalf("MarkSucceeded(attempt 1) err = %v, want ErrAttemptSuperseded", err)
	}
	active := activeFor(t, s, userID, docID)
	if len(active) != 1 || active[0].Status != "pending" {
		t.Errorf("attempt 2 should still be pending, got %+v", active)
	}
}

func TestWrites_UnknownJob_ReturnNotFound(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	k := key(uuid.New(), uuid.New(), queue.JobTypeCanonicalization)

	writes := map[string]func() error{
		"MarkProcessing": func() error { return s.MarkProcessing(ctx, k, 1) },
		"SetPhase":       func() error { return s.SetPhase(ctx, k, 1, "x") },
		"MarkSucceeded":  func() error { return s.MarkSucceeded(ctx, k, 1) },
		"MarkFailed":     func() error { return s.MarkFailed(ctx, k, 1, "x") },
	}
	for name, write := range writes {
		if err := write(); !errors.Is(err, jobstatus.ErrNotFound) {
			t.Errorf("%s err = %v, want ErrNotFound", name, err)
		}
	}
}

func TestTenantIsolation(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	owner, other, docID := uuid.New(), uuid.New(), uuid.New()

	if _, err := s.Enqueue(ctx, key(owner, docID, queue.JobTypeDocumentIndexing)); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	if active := activeFor(t, s, other, docID); len(active) != 0 {
		t.Errorf("another user sees %+v, want nothing", active)
	}
	if err := s.MarkFailed(ctx, key(other, docID, queue.JobTypeDocumentIndexing), 1, "x"); !errors.Is(err, jobstatus.ErrNotFound) {
		t.Errorf("another user's MarkFailed err = %v, want ErrNotFound", err)
	}
	if active := activeFor(t, s, owner, docID); len(active) != 1 || active[0].Status != "pending" {
		t.Errorf("owner's job changed by another user: %+v", active)
	}
}

func TestActiveJobsForDocuments_MultipleJobsAndDocuments(t *testing.T) {
	s := memory.New()
	ctx := context.Background()
	userID, docA, docB, docC := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	for _, k := range []jobstatus.Key{
		key(userID, docA, queue.JobTypeRegionClassification),
		key(userID, docA, queue.JobTypeEntityExtraction),
		key(userID, docB, queue.JobTypeDocumentIndexing),
	} {
		if _, err := s.Enqueue(ctx, k); err != nil {
			t.Fatalf("Enqueue %+v: %v", k, err)
		}
	}

	got, err := s.ActiveJobsForDocuments(ctx, userID, []uuid.UUID{docA, docB, docC})
	if err != nil {
		t.Fatalf("ActiveJobsForDocuments: %v", err)
	}
	if a := got[docA]; len(a) != 2 || a[0].Type != queue.JobTypeRegionClassification || a[1].Type != queue.JobTypeEntityExtraction {
		t.Errorf("docA = %+v, want region_classification then entity_extraction (enqueue order)", a)
	}
	if b := got[docB]; len(b) != 1 || b[0].Type != queue.JobTypeDocumentIndexing {
		t.Errorf("docB = %+v, want one document_indexing job", b)
	}
	if c := got[docC]; len(c) != 0 {
		t.Errorf("docC = %+v, want none", c)
	}
}

func TestActiveJobsForDocuments_NoIDs_ReturnsEmptyMap(t *testing.T) {
	got, err := memory.New().ActiveJobsForDocuments(context.Background(), uuid.New(), nil)
	if err != nil {
		t.Fatalf("ActiveJobsForDocuments: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got %v, want an empty non-nil map", got)
	}
}
