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
)

// DocumentHandler transitions document status through the processing lifecycle,
// splitting the raw file into chunks, embedding them, and persisting the index.
type DocumentHandler struct {
	docs     document.Repository
	objects  objectstore.ObjectStore
	chunks   chunk.Repository
	splitter chunk.Splitter
	embedder llm.Embedder
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

	return nil
}

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
		return nil
	}

	// Delete any existing chunks from a previous (partial) attempt.
	if err := h.chunks.DeleteByDocument(ctx, doc.UserID, doc.ID); err != nil {
		return fmt.Errorf("dochandler: clear existing chunks: %w", err)
	}

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

	for i, s := range splits {
		s.DocumentID = doc.ID
		s.KBID = doc.KBID
		s.UserID = doc.UserID
		s.Embedding = vecs[i]
	}

	if err := h.chunks.BulkCreate(ctx, splits); err != nil {
		return fmt.Errorf("dochandler: persist chunks: %w", err)
	}

	return nil
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
