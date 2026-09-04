package worker_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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

// TestEntityHandler_Handle_BatchesLargeChunkSets verifies chunks are sent
// to the Extractor in batches of at most WithBatchSize, not all at once —
// the fix for a large document's extraction taking long enough in one
// unbatched call to exceed JOB_STALE_TIMEOUT under normal operation.
func TestEntityHandler_Handle_BatchesLargeChunkSets(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 7)
	ctx := auth.WithUserID(context.Background(), userID)

	var batchSizes []int
	extractor := entitymock.New()
	extractor.ExtractFn = func(_ context.Context, batch []*chunk.Chunk, _ []entity.Type) ([]*entity.Entity, error) {
		batchSizes = append(batchSizes, len(batch))
		return nil, nil
	}

	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"}).
		WithBatchSize(3)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	want := []int{3, 3, 1}
	if len(batchSizes) != len(want) {
		t.Fatalf("Extract call count: got %d %v, want %d %v", len(batchSizes), batchSizes, len(want), want)
	}
	for i := range want {
		if batchSizes[i] != want[i] {
			t.Errorf("batch %d size: got %d, want %d", i, batchSizes[i], want[i])
		}
	}
}

// TestEntityHandler_Handle_PersistsPerBatch verifies entities from an
// earlier batch are persisted even when a later batch fails — proving
// persistence happens incrementally, not accumulated in memory and written
// only after every batch succeeds (which would lose everything on a
// mid-document failure, same as the pre-fix unbatched behavior).
func TestEntityHandler_Handle_PersistsPerBatch(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 4)
	ctx := auth.WithUserID(context.Background(), userID)

	storedChunks, err := chunks.ListByDocument(ctx, userID, job.DocumentID)
	if err != nil || len(storedChunks) != 4 {
		t.Fatalf("setup: expected 4 chunks, got %d (err: %v)", len(storedChunks), err)
	}

	callCount := 0
	extractor := entitymock.New()
	extractor.ExtractFn = func(_ context.Context, batch []*chunk.Chunk, _ []entity.Type) ([]*entity.Entity, error) {
		callCount++
		if callCount == 2 {
			return nil, errors.New("inference service unavailable")
		}
		out := make([]*entity.Entity, len(batch))
		for i, c := range batch {
			out[i] = &entity.Entity{DocumentID: job.DocumentID, KBID: c.KBID, ChunkID: c.ID, Type: "person", Text: "X", Start: 0, End: 1}
		}
		return out, nil
	}

	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"}).
		WithBatchSize(2)
	if err := h.Handle(ctx, job); err == nil {
		t.Fatal("expected an error from the second batch")
	}

	got, err := entities.ListByDocument(ctx, userID, job.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("entities persisted from the first (successful) batch: got %d, want 2", len(got))
	}
}

// TestEntityHandler_Handle_RetryResumesWithoutWipingOrReprocessing is the
// core regression test for the resume logic: a retry (Attempts > 0) of a
// job interrupted mid-document must not wipe entities already persisted by
// its earlier attempt, and must only send the still-unprocessed chunks to
// the Extractor — not the whole document again.
func TestEntityHandler_Handle_RetryResumesWithoutWipingOrReprocessing(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 4)
	ctx := auth.WithUserID(context.Background(), userID)

	// First attempt (Attempts == 0): batch 1 succeeds, batch 2 fails,
	// simulating an interruption partway through the document.
	callCount := 0
	firstExtractor := entitymock.New()
	firstExtractor.ExtractFn = func(_ context.Context, batch []*chunk.Chunk, _ []entity.Type) ([]*entity.Entity, error) {
		callCount++
		if callCount == 2 {
			return nil, errors.New("interrupted")
		}
		out := make([]*entity.Entity, len(batch))
		for i, c := range batch {
			out[i] = &entity.Entity{DocumentID: job.DocumentID, KBID: c.KBID, ChunkID: c.ID, Type: "person", Text: "X", Start: 0, End: 1}
		}
		return out, nil
	}
	h1 := worker.NewEntityHandler(docs, chunks, entities, firstExtractor, canonicalRepo, []string{"person"}).
		WithBatchSize(2)
	if err := h1.Handle(ctx, job); err == nil {
		t.Fatal("expected the first attempt to fail on its second batch")
	}
	afterFirstAttempt, _ := entities.ListByDocument(ctx, userID, job.DocumentID)
	if len(afterFirstAttempt) != 2 {
		t.Fatalf("entities after interrupted first attempt: got %d, want 2", len(afterFirstAttempt))
	}

	// Retry: same document, Attempts incremented — mirrors what Nack/
	// ReclaimStale redelivery actually does to the job row.
	retryJob := &queue.Job{ID: job.ID, Type: job.Type, DocumentID: job.DocumentID, UserID: job.UserID, Attempts: 1, MaxAttempts: job.MaxAttempts}

	var sentChunkIDs []uuid.UUID
	retryExtractor := entitymock.New()
	retryExtractor.ExtractFn = func(_ context.Context, batch []*chunk.Chunk, _ []entity.Type) ([]*entity.Entity, error) {
		out := make([]*entity.Entity, len(batch))
		for i, c := range batch {
			sentChunkIDs = append(sentChunkIDs, c.ID)
			out[i] = &entity.Entity{DocumentID: job.DocumentID, KBID: c.KBID, ChunkID: c.ID, Type: "person", Text: "Y", Start: 0, End: 1}
		}
		return out, nil
	}
	h2 := worker.NewEntityHandler(docs, chunks, entities, retryExtractor, canonicalRepo, []string{"person"}).
		WithBatchSize(2)
	if err := h2.Handle(ctx, retryJob); err != nil {
		t.Fatalf("retry Handle: %v", err)
	}

	if len(sentChunkIDs) != 2 {
		t.Fatalf("chunks sent to Extract on retry: got %d %v, want 2 (only the unprocessed ones)", len(sentChunkIDs), sentChunkIDs)
	}
	for _, id := range sentChunkIDs {
		for _, done := range afterFirstAttempt {
			if done.ChunkID == id {
				t.Errorf("retry re-sent chunk %v, which already had a persisted entity from the first attempt", id)
			}
		}
	}

	final, _ := entities.ListByDocument(ctx, userID, job.DocumentID)
	if len(final) != 4 {
		t.Fatalf("final entity count: got %d, want 4 (2 from first attempt + 2 from retry, none lost or duplicated)", len(final))
	}
}

// stubHeartbeater is a test double for worker.Heartbeater.
type stubHeartbeater struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (h *stubHeartbeater) Heartbeat(_ context.Context, jobID uuid.UUID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ids = append(h.ids, jobID)
	return nil
}

// TestEntityHandler_Handle_HeartbeatsPerBatch is the regression test for a
// gap found live: a long, multi-batch extraction kept getting reclaimed as
// stale — its attempt count burned — despite steadily persisting progress,
// because nothing touched the job row's updated_at between batches.
// WithHeartbeat must fire once per successfully persisted batch.
func TestEntityHandler_Handle_HeartbeatsPerBatch(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 7)
	ctx := auth.WithUserID(context.Background(), userID)

	extractor := entitymock.New()
	extractor.ExtractFn = func(_ context.Context, batch []*chunk.Chunk, _ []entity.Type) ([]*entity.Entity, error) {
		out := make([]*entity.Entity, len(batch))
		for i, c := range batch {
			out[i] = &entity.Entity{DocumentID: job.DocumentID, KBID: c.KBID, ChunkID: c.ID, Type: "person", Text: "X", Start: 0, End: 1}
		}
		return out, nil
	}
	heartbeats := &stubHeartbeater{}
	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"}).
		WithBatchSize(3).
		WithHeartbeat(heartbeats)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// 7 chunks at batch size 3 -> 3 batches (3, 3, 1) -> 3 heartbeats, all
	// for this job.
	if len(heartbeats.ids) != 3 {
		t.Fatalf("heartbeat calls: got %d, want 3", len(heartbeats.ids))
	}
	for _, id := range heartbeats.ids {
		if id != job.ID {
			t.Errorf("heartbeat called with job %v, want %v", id, job.ID)
		}
	}
}

// TestEntityHandler_Handle_NilHeartbeat_NoPanic confirms WithHeartbeat is
// genuinely optional.
func TestEntityHandler_Handle_NilHeartbeat_NoPanic(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 1)
	ctx := auth.WithUserID(context.Background(), userID)

	extractor := entitymock.NewFixed([]*entity.Entity{{Type: "person", Text: "Ada", Start: 0, End: 3}})
	// No WithHeartbeat call: heartbeat stays nil.
	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"})
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}
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

// TestEntityHandler_Handle_BatchExtractor_CallsExtractBatchesOnceWithAllBatches
// is the core test for why entity.BatchExtractor exists: when the
// Extractor supports it, Handle must hand it every batch in one call
// (letting the extractor submit all of them before waiting on any),
// rather than looping and calling plain Extract per batch.
func TestEntityHandler_Handle_BatchExtractor_CallsExtractBatchesOnceWithAllBatches(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 7)
	ctx := auth.WithUserID(context.Background(), userID)

	callCount := 0
	var gotBatchSizes []int
	extractor := entitymock.NewBatch()
	extractor.ExtractBatchesFn = func(_ context.Context, batches [][]*chunk.Chunk, _ []entity.Type) []entity.BatchResult {
		callCount++
		for _, b := range batches {
			gotBatchSizes = append(gotBatchSizes, len(b))
		}
		return make([]entity.BatchResult, len(batches))
	}

	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"}).
		WithBatchSize(3)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if callCount != 1 {
		t.Fatalf("ExtractBatches call count: got %d, want 1 (all batches in one call)", callCount)
	}
	want := []int{3, 3, 1}
	if len(gotBatchSizes) != len(want) {
		t.Fatalf("batch sizes: got %v, want %v", gotBatchSizes, want)
	}
	for i := range want {
		if gotBatchSizes[i] != want[i] {
			t.Errorf("batch %d size: got %d, want %d", i, gotBatchSizes[i], want[i])
		}
	}
}

// TestEntityHandler_Handle_BatchExtractor_PersistsSuccessfulBatchesDespiteOthersFailing
// verifies partial-failure handling for the concurrent path: one batch's
// extraction failing must not lose another batch's already-succeeded
// entities, mirroring the sequential path's
// TestEntityHandler_Handle_PersistsPerBatch guarantee.
func TestEntityHandler_Handle_BatchExtractor_PersistsSuccessfulBatchesDespiteOthersFailing(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 4)
	ctx := auth.WithUserID(context.Background(), userID)

	extractor := entitymock.NewBatch()
	extractor.ExtractBatchesFn = func(_ context.Context, batches [][]*chunk.Chunk, _ []entity.Type) []entity.BatchResult {
		results := make([]entity.BatchResult, len(batches))
		for i, batch := range batches {
			if i == 1 {
				results[i] = entity.BatchResult{Err: errors.New("inference service unavailable")}
				continue
			}
			out := make([]*entity.Entity, len(batch))
			for j, c := range batch {
				out[j] = &entity.Entity{DocumentID: job.DocumentID, KBID: c.KBID, ChunkID: c.ID, Type: "person", Text: "X", Start: 0, End: 1}
			}
			results[i] = entity.BatchResult{Entities: out}
		}
		return results
	}

	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"}).
		WithBatchSize(2)
	if err := h.Handle(ctx, job); err == nil {
		t.Fatal("expected an error surfaced from the failing batch")
	}

	got, err := entities.ListByDocument(ctx, userID, job.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("entities persisted from the succeeding batch: got %d, want 2", len(got))
	}
}

// TestEntityHandler_Handle_BatchExtractor_RetryOnlyResendsFailedBatchChunks
// mirrors TestEntityHandler_Handle_RetryResumesWithoutWipingOrReprocessing
// for the concurrent path: a retry must only re-send chunks from batches
// that failed, not ones a sibling batch already succeeded on.
func TestEntityHandler_Handle_BatchExtractor_RetryOnlyResendsFailedBatchChunks(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 4)
	ctx := auth.WithUserID(context.Background(), userID)

	firstExtractor := entitymock.NewBatch()
	firstExtractor.ExtractBatchesFn = func(_ context.Context, batches [][]*chunk.Chunk, _ []entity.Type) []entity.BatchResult {
		results := make([]entity.BatchResult, len(batches))
		for i, batch := range batches {
			if i == 1 {
				results[i] = entity.BatchResult{Err: errors.New("interrupted")}
				continue
			}
			out := make([]*entity.Entity, len(batch))
			for j, c := range batch {
				out[j] = &entity.Entity{DocumentID: job.DocumentID, KBID: c.KBID, ChunkID: c.ID, Type: "person", Text: "X", Start: 0, End: 1}
			}
			results[i] = entity.BatchResult{Entities: out}
		}
		return results
	}
	h1 := worker.NewEntityHandler(docs, chunks, entities, firstExtractor, canonicalRepo, []string{"person"}).
		WithBatchSize(2)
	if err := h1.Handle(ctx, job); err == nil {
		t.Fatal("expected the first attempt to fail on its second batch")
	}
	afterFirstAttempt, _ := entities.ListByDocument(ctx, userID, job.DocumentID)
	if len(afterFirstAttempt) != 2 {
		t.Fatalf("entities after interrupted first attempt: got %d, want 2", len(afterFirstAttempt))
	}

	retryJob := &queue.Job{ID: job.ID, Type: job.Type, DocumentID: job.DocumentID, UserID: job.UserID, Attempts: 1, MaxAttempts: job.MaxAttempts}

	var sentChunkIDs []uuid.UUID
	retryExtractor := entitymock.NewBatch()
	retryExtractor.ExtractBatchesFn = func(_ context.Context, batches [][]*chunk.Chunk, _ []entity.Type) []entity.BatchResult {
		results := make([]entity.BatchResult, len(batches))
		for i, batch := range batches {
			out := make([]*entity.Entity, len(batch))
			for j, c := range batch {
				sentChunkIDs = append(sentChunkIDs, c.ID)
				out[j] = &entity.Entity{DocumentID: job.DocumentID, KBID: c.KBID, ChunkID: c.ID, Type: "person", Text: "Y", Start: 0, End: 1}
			}
			results[i] = entity.BatchResult{Entities: out}
		}
		return results
	}
	h2 := worker.NewEntityHandler(docs, chunks, entities, retryExtractor, canonicalRepo, []string{"person"}).
		WithBatchSize(2)
	if err := h2.Handle(ctx, retryJob); err != nil {
		t.Fatalf("retry Handle: %v", err)
	}

	if len(sentChunkIDs) != 2 {
		t.Fatalf("chunks sent to ExtractBatches on retry: got %d %v, want 2 (only the unprocessed ones)", len(sentChunkIDs), sentChunkIDs)
	}
	for _, id := range sentChunkIDs {
		for _, done := range afterFirstAttempt {
			if done.ChunkID == id {
				t.Errorf("retry re-sent chunk %v, which already had a persisted entity from the first attempt", id)
			}
		}
	}

	final, _ := entities.ListByDocument(ctx, userID, job.DocumentID)
	if len(final) != 4 {
		t.Fatalf("final entity count: got %d, want 4 (2 from first attempt + 2 from retry, none lost or duplicated)", len(final))
	}
}

// TestEntityHandler_Handle_BatchExtractor_HeartbeatsOnTicker verifies the
// concurrent path heartbeats via its own ticker (there's no "between
// batches" point to hook into when every batch is awaited in one call),
// firing at least once during a call long enough to cross the configured
// interval.
func TestEntityHandler_Handle_BatchExtractor_HeartbeatsOnTicker(t *testing.T) {
	docs := docmem.New()
	chunks := chunkmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()
	job, userID := seedEntityJob(t, docs, chunks, 2)
	ctx := auth.WithUserID(context.Background(), userID)

	extractor := entitymock.NewBatch()
	extractor.ExtractBatchesFn = func(_ context.Context, batches [][]*chunk.Chunk, _ []entity.Type) []entity.BatchResult {
		time.Sleep(30 * time.Millisecond)
		return make([]entity.BatchResult, len(batches))
	}
	heartbeats := &stubHeartbeater{}

	h := worker.NewEntityHandler(docs, chunks, entities, extractor, canonicalRepo, []string{"person"}).
		WithHeartbeat(heartbeats).
		WithHeartbeatInterval(5 * time.Millisecond)
	if err := h.Handle(ctx, job); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	heartbeats.mu.Lock()
	got := len(heartbeats.ids)
	heartbeats.mu.Unlock()
	if got == 0 {
		t.Error("expected at least one heartbeat to fire during the concurrent ExtractBatches call")
	}
}
