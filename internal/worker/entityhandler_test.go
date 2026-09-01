package worker_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/canonical"
	canonicalmem "github.com/kunalpednekar/dumpster/internal/canonical/memory"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	chunkmem "github.com/kunalpednekar/dumpster/internal/chunk/memory"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/entity"
	entitymem "github.com/kunalpednekar/dumpster/internal/entity/memory"
	entitymock "github.com/kunalpednekar/dumpster/internal/entity/mock"
	"github.com/kunalpednekar/dumpster/internal/queue"
	qmem "github.com/kunalpednekar/dumpster/internal/queue/memory"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

// seedEntityJob creates a document (and optionally chunks for it) and
// returns a matching entity-extraction Job.
func seedEntityJob(t *testing.T, docs *docmem.Repository, chunks *chunkmem.Repository, numChunks int) (*queue.Job, uuid.UUID) {
	t.Helper()
	userID := uuid.New()
	ctx := auth.WithUserID(context.Background(), userID)

	doc, err := docs.Create(ctx, &document.Document{
		KBID:        uuid.New(),
		UserID:      userID,
		Filename:    "test.txt",
		S3Key:       "uploads/" + uuid.New().String(),
		ContentType: "text/plain",
		Status:      document.StatusIndexed,
	})
	if err != nil {
		t.Fatal(err)
	}

	if numChunks > 0 {
		cs := make([]*chunk.Chunk, numChunks)
		for i := range cs {
			cs[i] = &chunk.Chunk{
				DocumentID: doc.ID,
				KBID:       doc.KBID,
				UserID:     userID,
				Ordinal:    i,
				Text:       "Ada Lovelace worked with Charles Babbage.",
				CharStart:  i * 100,
				CharEnd:    i*100 + 42,
			}
		}
		if err := chunks.BulkCreate(ctx, cs); err != nil {
			t.Fatal(err)
		}
	}

	return &queue.Job{
		ID:          uuid.New(),
		Type:        queue.JobTypeEntityExtraction,
		DocumentID:  doc.ID,
		UserID:      userID,
		Attempts:    0,
		MaxAttempts: 3,
	}, userID
}

func TestEntityHandler_Handle_PersistsExtractedEntities(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 2)
	ctx := auth.WithUserID(context.Background(), userID)

	storedChunks, err := chunks.ListByDocument(ctx, userID, job.DocumentID)
	if err != nil || len(storedChunks) != 2 {
		t.Fatalf("setup: expected 2 chunks, got %d (err: %v)", len(storedChunks), err)
	}

	// A real Extractor populates DocumentID/KBID/ChunkID from the input
	// chunk (see internal/entity/inference); mimic that contract here.
	extractor := entitymock.NewFixed([]*entity.Entity{
		{
			DocumentID: job.DocumentID,
			KBID:       storedChunks[0].KBID,
			ChunkID:    storedChunks[0].ID,
			Type:       "person", Text: "Ada Lovelace", Start: 0, End: 12, Score: 0.9,
		},
	})

	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person", "organization"})
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got, err := entities.ListByDocument(ctx, userID, job.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("entities persisted: got %d, want 1", len(got))
	}
	if got[0].UserID != userID {
		t.Errorf("entity UserID not backfilled from job: got %v, want %v", got[0].UserID, userID)
	}
	if got[0].Text != "Ada Lovelace" || got[0].Type != "person" {
		t.Errorf("unexpected entity: %+v", got[0])
	}
}

func TestEntityHandler_Handle_DoesNotReChunkOrReEmbed(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 3)
	ctx := auth.WithUserID(context.Background(), userID)

	before, err := chunks.ListByDocument(ctx, userID, job.DocumentID)
	if err != nil {
		t.Fatal(err)
	}

	extractor := entitymock.NewFixed([]*entity.Entity{{Type: "person", Text: "X", Start: 0, End: 1}})
	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"})
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	after, err := chunks.ListByDocument(ctx, userID, job.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("chunk count changed: before=%d after=%d", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Text != after[i].Text || before[i].CharStart != after[i].CharStart {
			t.Errorf("chunk %d mutated by entity extraction: before=%+v after=%+v", i, before[i], after[i])
		}
	}
}

func TestEntityHandler_Handle_RerunIsIdempotent(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 1)
	ctx := auth.WithUserID(context.Background(), userID)

	extractor := entitymock.NewFixed([]*entity.Entity{{Type: "person", Text: "Ada", Start: 0, End: 3}})
	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"})

	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	first, _ := entities.ListByDocument(ctx, userID, job.DocumentID)

	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	second, _ := entities.ListByDocument(ctx, userID, job.DocumentID)

	if len(first) != len(second) {
		t.Errorf("re-run: entity count changed from %d to %d", len(first), len(second))
	}
}

func TestEntityHandler_Handle_NoChunksYet_NoOp(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 0) // document exists, no chunks
	ctx := auth.WithUserID(context.Background(), userID)

	extractCalled := false
	extractor := entitymock.New()
	extractor.ExtractFn = func(_ context.Context, _ []*chunk.Chunk, _ []entity.Type) ([]*entity.Entity, error) {
		extractCalled = true
		return nil, nil
	}

	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"})
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if extractCalled {
		t.Error("extractor should not be invoked when the document has no chunks")
	}
}

func TestEntityHandler_Handle_ExtractError(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 1)
	ctx := auth.WithUserID(context.Background(), userID)

	extractor := entitymock.NewError(errors.New("extraction sidecar unavailable"))
	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"})

	if err := h.Handle(ctx, job); err == nil {
		t.Fatal("expected error from extractor")
	}

	got, _ := entities.ListByDocument(ctx, userID, job.DocumentID)
	if len(got) != 0 {
		t.Errorf("no entities should be persisted on extractor error, got %d", len(got))
	}
}

func TestEntityHandler_Handle_UnknownDocument(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	extractor := entitymock.New()
	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"})

	userID := uuid.New()
	job := &queue.Job{ID: uuid.New(), Type: queue.JobTypeEntityExtraction, DocumentID: uuid.New(), UserID: userID}
	ctx := auth.WithUserID(context.Background(), userID)

	if err := h.Handle(ctx, job); err == nil {
		t.Fatal("expected error for unknown document")
	}
}

func TestEntityHandler_OnFailed_DoesNotErrorOrPanic(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	extractor := entitymock.New()
	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"})

	job := &queue.Job{ID: uuid.New(), Type: queue.JobTypeEntityExtraction, DocumentID: uuid.New(), UserID: uuid.New()}
	h.OnFailed(context.Background(), job) // must not panic
}

func TestEntityHandler_PublishesEdgeExtractionAfterPersist(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	pub := qmem.New()

	job, userID := seedEntityJob(t, docs, chunks, 1)
	ctx := auth.WithUserID(context.Background(), userID)

	extractor := entitymock.NewFixed([]*entity.Entity{{Type: "person", Text: "Ada", Start: 0, End: 3, Score: 0.9}})
	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"}).
		WithDownstreamPublisher(pub)

	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	evts := pub.EdgeExtractionEvents()
	if len(evts) != 1 {
		t.Fatalf("expected 1 EdgeExtraction event published, got %d", len(evts))
	}
	if evts[0].DocumentID != job.DocumentID {
		t.Errorf("event DocumentID: got %v, want %v", evts[0].DocumentID, job.DocumentID)
	}

	canonEvts := pub.CanonicalizationEvents()
	if len(canonEvts) != 1 {
		t.Fatalf("expected 1 Canonicalization event published, got %d", len(canonEvts))
	}
	if canonEvts[0].DocumentID != job.DocumentID {
		t.Errorf("event DocumentID: got %v, want %v", canonEvts[0].DocumentID, job.DocumentID)
	}
}

func TestEntityHandler_NilPublisher_NoEdgePublish(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()

	job, userID := seedEntityJob(t, docs, chunks, 1)
	ctx := auth.WithUserID(context.Background(), userID)

	extractor := entitymock.NewFixed([]*entity.Entity{{Type: "person", Text: "Ada", Start: 0, End: 3}})
	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"})
	// No publisher wired — must not panic.
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

func TestEntityHandler_ImplementsHandler(t *testing.T) {
	var _ worker.Handler = (*worker.EntityHandler)(nil)
}

// TestEntityHandler_Handle_DecrementsCanonicalStatsBeforeReplacingEntities
// guards the reason canonicalization needs its own idempotency handling
// (see canonical.Repository.DecrementForDocument): canonical_entities counts
// are cross-document state, so the delete-and-recreate pattern that makes
// entities/edges idempotent under a re-run does not by itself keep
// canonical stats correct — without an explicit decrement, a document's old
// contribution would linger forever once its entity rows are replaced.
func TestEntityHandler_Handle_DecrementsCanonicalStatsBeforeReplacingEntities(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()

	job, userID := seedEntityJob(t, docs, chunks, 1)
	ctx := auth.WithUserID(context.Background(), userID)

	storedChunks, err := chunks.ListByDocument(ctx, userID, job.DocumentID)
	if err != nil || len(storedChunks) != 1 {
		t.Fatalf("setup: expected 1 chunk, got %d (err: %v)", len(storedChunks), err)
	}
	extractor := entitymock.NewFixed([]*entity.Entity{{
		DocumentID: job.DocumentID, KBID: storedChunks[0].KBID, ChunkID: storedChunks[0].ID,
		Type: "person", Text: "Ada", Start: 0, End: 3, Score: 0.9,
	}})
	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"})
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("first Handle: %v", err)
	}

	// Simulate the (normally async) canonicalization job stage having run
	// for this document.
	mentions, _ := entities.ListByDocument(ctx, userID, job.DocumentID)
	if err := canonical.ResolveNew(ctx, canonicalRepo, entities, userID, mentions); err != nil {
		t.Fatal(err)
	}
	afterFirst, _ := entities.ListByDocument(ctx, userID, job.DocumentID)
	if len(afterFirst) != 1 || afterFirst[0].CanonicalEntityID == nil {
		t.Fatalf("setup: expected the mention to be canonicalized, got %+v", afterFirst)
	}
	canonicalID := *afterFirst[0].CanonicalEntityID
	if ce, err := canonicalRepo.Get(ctx, userID, canonicalID); err != nil || ce.MentionCount != 1 {
		t.Fatalf("setup: expected mention_count 1, got ce=%+v err=%v", ce, err)
	}

	// Re-run extraction for the same document (e.g. a type-set change) with
	// an extractor that finds nothing this time.
	emptyExtractor := entitymock.New()
	h2 := worker.NewEntityHandler(docs, chunks, entities, emptyExtractor, canonicalRepo, []string{"person"})
	if err := h2.Handle(ctx, job); err != nil {
		t.Fatalf("second Handle: %v", err)
	}

	if _, err := canonicalRepo.Get(ctx, userID, canonicalID); err != canonical.ErrNotFound {
		t.Errorf("expected the canonical entity to be cleaned up once its only contributing document's re-run found nothing, got err=%v", err)
	}
}
