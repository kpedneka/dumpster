package worker

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/llm"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/stats"
)

// DocumentHandler transitions document status through the processing lifecycle,
// splitting the raw file into chunks, embedding them, and persisting the index.
type DocumentHandler struct {
	docs     document.Repository
	objects  objectstore.ObjectStore
	chunks   chunk.Repository
	splitter chunk.Splitter
	embedder llm.Embedder
	// publisher, when non-nil, is used to enqueue a distinct entity-
	// extraction job once a document is successfully indexed. It is
	// optional so existing callers/tests that only care about chunking and
	// embedding keep working unmodified.
	publisher queue.Publisher
	// stats, when non-nil, records a successful index into the durable,
	// cross-tenant usage counters (see internal/stats). Optional for the
	// same reason publisher is.
	stats stats.Repository
}

// NewDocumentHandler creates a DocumentHandler wired to the given dependencies.
func NewDocumentHandler(
	docs document.Repository,
	objects objectstore.ObjectStore,
	chunks chunk.Repository,
	splitter chunk.Splitter,
	embedder llm.Embedder,
) *DocumentHandler {
	return &DocumentHandler{
		docs:     docs,
		objects:  objects,
		chunks:   chunks,
		splitter: splitter,
		embedder: embedder,
	}
}

// WithEntityExtractionPublisher wires publisher into h so that a distinct
// entity-extraction job can be queued for a document behind the existing
// queue/job-stage seam. Queued from inside process(), right after chunks
// are persisted and before the embed call — see process()'s doc comment
// for why — not after the document reaches Indexed. This keeps entity
// extraction inline in the ingestion flow without coupling DocumentHandler
// to how extraction itself works.
func (h *DocumentHandler) WithEntityExtractionPublisher(publisher queue.Publisher) *DocumentHandler {
	h.publisher = publisher
	return h
}

// WithStats wires repo into h so that every document this handler
// successfully indexes is recorded into the durable, cross-tenant usage
// counters. Optional, matching WithEntityExtractionPublisher.
func (h *DocumentHandler) WithStats(repo stats.Repository) *DocumentHandler {
	h.stats = repo
	return h
}

// Handle runs the full ingestion pipeline for one document job:
// pending → processing → (read, split, embed, persist) → indexed.
// A document that is already indexed is skipped (idempotent).
func (h *DocumentHandler) Handle(ctx context.Context, job *queue.Job) error {
	ctx = auth.WithUserID(ctx, job.UserID)

	doc, err := h.docs.Get(ctx, job.UserID, job.DocumentID)
	if err != nil {
		return fmt.Errorf("dochandler: get document %s: %w", job.DocumentID, err)
	}

	if doc.Status == document.StatusIndexed {
		return nil
	}

	if err := h.docs.UpdateStatus(ctx, job.UserID, job.DocumentID, document.StatusProcessing); err != nil {
		return fmt.Errorf("dochandler: mark processing: %w", err)
	}

	if err := h.process(ctx, doc); err != nil {
		return err
	}

	if err := h.docs.UpdateStatus(ctx, job.UserID, job.DocumentID, document.StatusIndexed); err != nil {
		return fmt.Errorf("dochandler: mark indexed: %w", err)
	}

	// A stats-recording failure is logged, not fatal, for the same reason
	// a publish failure below isn't: indexing has already succeeded and
	// must not be rolled back because a side accounting write failed.
	if h.stats != nil {
		if err := h.stats.RecordDocumentIndexed(ctx, doc.SizeBytes); err != nil {
			log.Printf("dochandler: failed to record usage stats for document %s: %v", job.DocumentID, err)
		}
	}

	// Entity extraction is published from inside process(), right after
	// chunks are persisted, not here -- see process()'s doc comment for
	// why. Publishing it again here would be at best redundant and at
	// worst a duplicate entity-extraction run: the jobs table only
	// deduplicates concurrent (document_id, job_type) pairs while one is
	// still pending/processing (see migrations/012_entity_extraction.sql),
	// so if process()'s early job already finished and was Acked by the
	// time Handle() reached this point, a second publish here would
	// enqueue a genuine second run.

	return nil
}

// process reads the source file, splits it into chunks, persists them,
// publishes entity extraction, embeds the chunks, and backfills their
// embeddings -- in that order. Chunks are persisted (with a nil
// embedding) and entity extraction published *before* the embed call,
// not after: entity extraction only needs chunk text, not embedding
// vectors, so publishing it early lets a second worker goroutine dequeue
// and start extracting while this call is still blocked on the embed AWS
// Batch job, rather than only starting once this whole function returns.
func (h *DocumentHandler) process(ctx context.Context, doc *document.Document) error {
	rc, err := h.objects.Get(ctx, doc.S3Key)
	if err != nil {
		return fmt.Errorf("dochandler: read object %s: %w", doc.S3Key, err)
	}
	defer func() {
		if cerr := rc.Close(); cerr != nil {
			log.Printf("dochandler: close object %s: %v", doc.S3Key, cerr)
		}
	}()

	data, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("dochandler: read content: %w", err)
	}

	splits := h.splitter.Split(string(data))
	if len(splits) == 0 {
		h.publishEntityExtraction(ctx, doc)
		return nil
	}

	// Delete any existing chunks from a previous (partial) attempt.
	if err := h.chunks.DeleteByDocument(ctx, doc.UserID, doc.ID); err != nil {
		return fmt.Errorf("dochandler: clear existing chunks: %w", err)
	}

	for _, s := range splits {
		s.DocumentID = doc.ID
		s.KBID = doc.KBID
		s.UserID = doc.UserID
	}

	// Persist chunks now, with Embedding left nil, rather than waiting
	// until after the embed call below returns -- see process()'s doc
	// comment. A nil embedding is already a normal state chunks support
	// (migrations/019_local_embeddings.sql: such a chunk simply drops
	// out of vector search, keyword search still covers it).
	if err := h.chunks.BulkCreate(ctx, splits); err != nil {
		return fmt.Errorf("dochandler: persist chunks: %w", err)
	}

	h.publishEntityExtraction(ctx, doc)

	texts := make([]string, len(splits))
	for i, s := range splits {
		texts[i] = s.Text
	}

	vecs, err := h.embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("dochandler: embed: %w", err)
	}
	if len(vecs) != len(splits) {
		return fmt.Errorf("dochandler: embedder returned %d vectors for %d chunks", len(vecs), len(splits))
	}

	// Backfill each chunk's embedding by re-listing (ordered by ordinal,
	// matching the order splits/texts/vecs were built in) rather than
	// using splits directly -- BulkCreate never assigns generated IDs
	// back onto its input slice, so this is the only way to learn which
	// database row each vector belongs to. Same insert-then-backfill
	// pattern internal/reembed/reembed.go already uses.
	persisted, err := h.chunks.ListByDocument(ctx, doc.UserID, doc.ID)
	if err != nil {
		return fmt.Errorf("dochandler: list chunks for embedding backfill: %w", err)
	}
	if len(persisted) != len(vecs) {
		return fmt.Errorf("dochandler: chunk/embedding count mismatch: %d persisted chunks, %d vectors", len(persisted), len(vecs))
	}
	for i, c := range persisted {
		if err := h.chunks.UpdateEmbedding(ctx, doc.UserID, c.ID, vecs[i]); err != nil {
			return fmt.Errorf("dochandler: update embedding for chunk %s: %w", c.ID, err)
		}
	}

	return nil
}

// publishEntityExtraction enqueues the entity-extraction stage for doc.
// Called from inside process() -- right after chunks are persisted,
// whether or not there are any to embed -- rather than from Handle()
// after the document reaches Indexed, so entity extraction can run
// concurrently with the embed call above instead of waiting for it.
func (h *DocumentHandler) publishEntityExtraction(ctx context.Context, doc *document.Document) {
	if h.publisher == nil {
		return
	}
	if err := h.publisher.PublishEntityExtraction(ctx, queue.EntityExtractionRequested{
		DocumentID: doc.ID,
		UserID:     doc.UserID,
	}); err != nil {
		log.Printf("dochandler: failed to queue entity extraction for document %s: %v", doc.ID, err)
	}
}

// OnFailed marks the document as failed when the job is dead-lettered. The
// source file is retained in object storage so a re-index can reprocess the
// same object without re-upload.
func (h *DocumentHandler) OnFailed(ctx context.Context, job *queue.Job) {
	ctx = auth.WithUserID(ctx, job.UserID)
	if err := h.docs.UpdateStatus(ctx, job.UserID, job.DocumentID, document.StatusFailed); err != nil {
		log.Printf("dochandler: failed to mark document %s as failed: %v", job.DocumentID, err)
	}
}

var _ Handler = (*DocumentHandler)(nil)
