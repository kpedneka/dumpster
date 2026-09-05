package worker_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	chunkmem "github.com/kunalpednekar/dumpster/internal/chunk/memory"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/llm/mock"
	objmock "github.com/kunalpednekar/dumpster/internal/objectstore/mock"
	"github.com/kunalpednekar/dumpster/internal/queue"
	queuemem "github.com/kunalpednekar/dumpster/internal/queue/memory"
	statsmem "github.com/kunalpednekar/dumpster/internal/stats/memory"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

const testDims = 4

// publisherSpy wraps a queue.Publisher, invoking onPublishEntityExtraction
// (if set) synchronously before delegating -- used to observe handler
// state (call order relative to Embed, DB state) at the exact moment
// entity extraction is enqueued. Shared by dochandler_test.go and
// regionhandler_test.go (both package worker_test).
type publisherSpy struct {
	*queuemem.Publisher
	onPublishEntityExtraction func()
}

func (p *publisherSpy) PublishEntityExtraction(ctx context.Context, evt queue.EntityExtractionRequested) error {
	if p.onPublishEntityExtraction != nil {
		p.onPublishEntityExtraction()
	}
	return p.Publisher.PublishEntityExtraction(ctx, evt)
}

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

func TestDocumentHandler_Handle_PublishesEntityExtractionOnIndexed(t *testing.T) {
	docs := docmem.New()
	objects := objmock.New()
	chunks := chunkmem.New()
	publisher := queuemem.New()

	content := makeText(50)
	job, userID := makeDocJob(t, docs, objects, document.StatusPending, content)
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewDocumentHandler(docs, objects, chunks, chunk.NewFixedWindow(100, 10), mock.NewEmbedder(testDims)).
		WithEntityExtractionPublisher(publisher)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	events := publisher.EntityExtractionEvents()
	if len(events) != 1 {
		t.Fatalf("entity extraction events: got %d, want 1", len(events))
	}
	if events[0].DocumentID != job.DocumentID || events[0].UserID != userID {
		t.Errorf("unexpected event: %+v", events[0])
	}
}

// TestDocumentHandler_Handle_PublishesEntityExtractionBeforeEmbed proves
// the property this handler's parallelization depends on: entity
// extraction is enqueued before the (potentially slow, AWS-Batch-backed)
// embed call runs, not after -- so a second worker goroutine can dequeue
// and start extracting while this one is still blocked waiting on Embed,
// instead of only starting once the whole document is already indexed.
func TestDocumentHandler_Handle_PublishesEntityExtractionBeforeEmbed(t *testing.T) {
	docs := docmem.New()
	objects := objmock.New()
	chunks := chunkmem.New()

	var order []string
	embedder := mock.NewEmbedder(testDims)
	embedder.EmbedFn = func(_ context.Context, texts []string) ([][]float32, error) {
		order = append(order, "embed")
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = make([]float32, testDims)
		}
		return out, nil
	}
	publisher := &publisherSpy{
		Publisher: queuemem.New(),
		onPublishEntityExtraction: func() {
			order = append(order, "publish_entity_extraction")
		},
	}

	content := makeText(50)
	job, userID := makeDocJob(t, docs, objects, document.StatusPending, content)
	ctx := auth.WithUserID(context.Background(), userID)

	h := worker.NewDocumentHandler(docs, objects, chunks, chunk.NewFixedWindow(100, 10), embedder).
		WithEntityExtractionPublisher(publisher)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(order) != 2 || order[0] != "publish_entity_extraction" || order[1] != "embed" {
		t.Errorf("call order = %v, want [publish_entity_extraction embed]", order)
	}
}

// TestDocumentHandler_Handle_ChunksPersistedBeforeEntityExtractionPublished
// proves entity extraction has real chunk rows to read the moment it's
// published, with their embedding still nil (embed hasn't run yet) --
// the actual precondition that makes concurrent extraction safe, not
// just an ordering coincidence.
func TestDocumentHandler_Handle_ChunksPersistedBeforeEntityExtractionPublished(t *testing.T) {
	docs := docmem.New()
	objects := objmock.New()
	chunks := chunkmem.New()

	content := makeText(50)
	job, userID := makeDocJob(t, docs, objects, document.StatusPending, content)
	ctx := auth.WithUserID(context.Background(), userID)

	// chunkmem.ListByDocument returns pointers to its own internal
	// records, which UpdateEmbedding later mutates in place -- so texts
	// and embedding lengths must be copied out *inside* the callback,
	// not just the []*chunk.Chunk slice, or this would observe the final
	// post-backfill state instead of the moment of publish.
	var chunkCount int
	var emptyTexts, nonNilEmbeddings int
	publisher := &publisherSpy{
		Publisher: queuemem.New(),
		onPublishEntityExtraction: func() {
			snapshot, _ := chunks.ListByDocument(ctx, userID, job.DocumentID)
			chunkCount = len(snapshot)
			for _, c := range snapshot {
				if c.Text == "" {
					emptyTexts++
				}
				if len(c.Embedding) != 0 {
					nonNilEmbeddings++
				}
			}
		},
	}

	h := worker.NewDocumentHandler(docs, objects, chunks, chunk.NewFixedWindow(100, 10), mock.NewEmbedder(testDims)).
		WithEntityExtractionPublisher(publisher)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if chunkCount == 0 {
		t.Fatal("expected chunks to already be persisted at the moment entity extraction is published")
	}
	if emptyTexts != 0 {
		t.Errorf("expected all chunk text to already be set, got %d empty", emptyTexts)
	}
	if nonNilEmbeddings != 0 {
		t.Errorf("expected embedding to still be nil at publish time (embed hasn't run yet), got %d non-nil", nonNilEmbeddings)
	}
}

func TestDocumentHandler_Handle_NilPublisher_NoPanic(t *testing.T) {
	docs := docmem.New()
	objects := objmock.New()
	chunks := chunkmem.New()

	content := makeText(20)
	job, userID := makeDocJob(t, docs, objects, document.StatusPending, content)
	ctx := auth.WithUserID(context.Background(), userID)

	// No WithEntityExtractionPublisher call: publisher stays nil.
	h := worker.NewDocumentHandler(docs, objects, chunks, chunk.NewFixedWindow(100, 10), mock.NewEmbedder(testDims))
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

func TestDocumentHandler_Handle_RecordsStatsOnIndexed(t *testing.T) {
	docs := docmem.New()
	objects := objmock.New()
	chunks := chunkmem.New()
	statsRepo := statsmem.New()

	userID := uuid.New()
	ctx := auth.WithUserID(context.Background(), userID)
	content := makeText(50)
	s3Key := "uploads/" + uuid.New().String()
	doc, err := docs.Create(ctx, &document.Document{
		KBID: uuid.New(), UserID: userID, Filename: "test.txt",
		S3Key: s3Key, ContentType: "text/plain", SizeBytes: int64(len(content)),
		Status: document.StatusPending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(ctx, s3Key, bytes.NewReader([]byte(content)), int64(len(content)), "text/plain"); err != nil {
		t.Fatal(err)
	}
	job := &queue.Job{ID: uuid.New(), DocumentID: doc.ID, UserID: userID, MaxAttempts: 3}

	h := worker.NewDocumentHandler(docs, objects, chunks, chunk.NewFixedWindow(100, 10), mock.NewEmbedder(testDims)).
		WithStats(statsRepo)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	snap, err := statsRepo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if snap.DocumentsIndexed != 1 {
		t.Errorf("DocumentsIndexed = %d, want 1", snap.DocumentsIndexed)
	}
	if snap.AvgDocumentSizeBytes != float64(len(content)) {
		t.Errorf("AvgDocumentSizeBytes = %v, want %v", snap.AvgDocumentSizeBytes, len(content))
	}
}

func TestDocumentHandler_Handle_NilStats_NoPanic(t *testing.T) {
	docs := docmem.New()
	objects := objmock.New()
	chunks := chunkmem.New()

	content := makeText(20)
	job, userID := makeDocJob(t, docs, objects, document.StatusPending, content)
	ctx := auth.WithUserID(context.Background(), userID)

	// No WithStats call: stats stays nil.
	h := worker.NewDocumentHandler(docs, objects, chunks, chunk.NewFixedWindow(100, 10), mock.NewEmbedder(testDims))
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}
