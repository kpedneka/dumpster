package worker_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	chunkmem "github.com/kunalpednekar/dumpster/internal/chunk/memory"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/llm/mock"
	objmock "github.com/kunalpednekar/dumpster/internal/objectstore/mock"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/worker"
	"github.com/kunalpednekar/dumpster/internal/chunk"
)

const testDims = 4

// makeDocJob creates a document seeded into docs and objects, returning a matching Job.
func makeDocJob(t *testing.T, docs *docmem.Repository, objects *objmock.Store, status document.Status, content string) (*queue.Job, uuid.UUID) {
	t.Helper()
	userID := uuid.New()
	ctx := auth.WithUserID(context.Background(), userID)
	s3Key := "uploads/" + uuid.New().String()
	doc, err := docs.Create(ctx, &document.Document{
		KBID:        uuid.New(),
		UserID:      userID,
		Filename:    "test.txt",
		S3Key:       s3Key,
		ContentType: "text/plain",
		Status:      status,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(ctx, s3Key, bytes.NewReader([]byte(content)), int64(len(content)), "text/plain"); err != nil {
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

func newHandler(docs *docmem.Repository, objects *objmock.Store, chunks *chunkmem.Repository, splitter chunk.Splitter) *worker.DocumentHandler {
	return worker.NewDocumentHandler(docs, objects, chunks, splitter, mock.NewEmbedder(testDims))
}

func TestDocumentHandler_Handle_PendingToIndexed(t *testing.T) {
	docs := docmem.New()
	objects := objmock.New()
	chunks := chunkmem.New()
	job, userID := makeDocJob(t, docs, objects, document.StatusPending, "")
	ctx := auth.WithUserID(context.Background(), userID)

	h := newHandler(docs, objects, chunks, chunk.NewFixedWindow(100, 10))
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
	objects := objmock.New()
	chunks := chunkmem.New()
	job, userID := makeDocJob(t, docs, objects, document.StatusIndexed, "")
	ctx := auth.WithUserID(context.Background(), userID)

	h := newHandler(docs, objects, chunks, chunk.NewFixedWindow(100, 10))
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
	objects := objmock.New()
	chunks := chunkmem.New()
	job, userID := makeDocJob(t, docs, objects, document.StatusProcessing, "")
	ctx := auth.WithUserID(context.Background(), userID)

	h := newHandler(docs, objects, chunks, chunk.NewFixedWindow(100, 10))
	h.OnFailed(ctx, job)

	got, err := docs.Get(ctx, userID, job.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != document.StatusFailed {
		t.Errorf("status: got %q, want %q", got.Status, document.StatusFailed)
	}
}

func TestDocumentHandler_Handle_SplitsAndEmbeds(t *testing.T) {
	docs := docmem.New()
	objects := objmock.New()
	chunks := chunkmem.New()

	// 250-char text with 100-char window → 3 chunks (step=90)
	content := makeText(250)
	job, userID := makeDocJob(t, docs, objects, document.StatusPending, content)
	ctx := auth.WithUserID(context.Background(), userID)

	h := newHandler(docs, objects, chunks, chunk.NewFixedWindow(100, 10))
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	stored, err := chunks.ListByDocument(ctx, userID, job.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) == 0 {
		t.Fatal("expected chunks to be persisted, got 0")
	}
	for i, c := range stored {
		if len(c.Embedding) != testDims {
			t.Errorf("chunk %d: embedding length %d, want %d", i, len(c.Embedding), testDims)
		}
		// verify offset recovery
		if content[c.CharStart:c.CharEnd] != c.Text {
			t.Errorf("chunk %d: offset recovery failed", i)
		}
	}
}

func TestDocumentHandler_Handle_IdempotentOnRerun(t *testing.T) {
	docs := docmem.New()
	objects := objmock.New()
	chunks := chunkmem.New()

	content := makeText(200)
	job, userID := makeDocJob(t, docs, objects, document.StatusPending, content)
	ctx := auth.WithUserID(context.Background(), userID)

	h := newHandler(docs, objects, chunks, chunk.NewFixedWindow(100, 10))

	// First run
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	firstCount, _ := chunks.ListByDocument(ctx, userID, job.DocumentID)

	// Reset to pending to simulate a retry
	if err := docs.UpdateStatus(ctx, userID, job.DocumentID, document.StatusPending); err != nil {
		t.Fatal(err)
	}

	// Second run
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	secondCount, _ := chunks.ListByDocument(ctx, userID, job.DocumentID)

	if len(firstCount) != len(secondCount) {
		t.Errorf("re-run: chunk count changed from %d to %d", len(firstCount), len(secondCount))
	}
}

func TestDocumentHandler_Handle_EmbedError(t *testing.T) {
	docs := docmem.New()
	objects := objmock.New()
	chunks := chunkmem.New()

	content := makeText(50)
	job, userID := makeDocJob(t, docs, objects, document.StatusPending, content)
	ctx := auth.WithUserID(context.Background(), userID)

	embedder := mock.NewEmbedder(testDims)
	embedder.EmbedFn = func(_ context.Context, _ []string) ([][]float32, error) {
		return nil, context.DeadlineExceeded
	}
	h := worker.NewDocumentHandler(docs, objects, chunks, chunk.NewFixedWindow(100, 10), embedder)
	if err := h.Handle(ctx, job); err == nil {
		t.Fatal("expected error from embedder, got nil")
	}
}

func makeText(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return string(b)
}
