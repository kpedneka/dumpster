package worker_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

// makeDocJob creates a document in docs with the given status and returns a
// matching Job and the document's owner userID.
func makeDocJob(t *testing.T, docs *docmem.Repository, status document.Status) (*queue.Job, uuid.UUID) {
	t.Helper()
	userID := uuid.New()
	ctx := auth.WithUserID(context.Background(), userID)
	doc, err := docs.Create(ctx, &document.Document{
		KBID:        uuid.New(),
		UserID:      userID,
		Filename:    "test.txt",
		S3Key:       "key/" + uuid.New().String(),
		ContentType: "text/plain",
		Status:      status,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &queue.Job{
		ID:          uuid.New(),
		DocumentID:  doc.ID,
		UserID:      userID,
		Attempts:    0,
		MaxAttempts: 3,
	}, userID
}

func TestDocumentHandler_Handle_PendingToIndexed(t *testing.T) {
	docs := docmem.New()
	job, userID := makeDocJob(t, docs, document.StatusPending)
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewDocumentHandler(docs)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got, err := docs.Get(ctx, userID, job.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != document.StatusIndexed {
		t.Errorf("status: got %q, want %q", got.Status, document.StatusIndexed)
	}
}

func TestDocumentHandler_Handle_AlreadyIndexed_Idempotent(t *testing.T) {
	docs := docmem.New()
	job, userID := makeDocJob(t, docs, document.StatusIndexed)
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewDocumentHandler(docs)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got, err := docs.Get(ctx, userID, job.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != document.StatusIndexed {
		t.Errorf("status: got %q, want %q", got.Status, document.StatusIndexed)
	}
}

func TestDocumentHandler_OnFailed_MarksDocFailed(t *testing.T) {
	docs := docmem.New()
	job, userID := makeDocJob(t, docs, document.StatusProcessing)
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewDocumentHandler(docs)
	h.OnFailed(ctx, job)

	got, err := docs.Get(ctx, userID, job.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != document.StatusFailed {
		t.Errorf("status: got %q, want %q", got.Status, document.StatusFailed)
	}
}
