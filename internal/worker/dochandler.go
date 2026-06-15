package worker

import (
	"context"
	"fmt"
	"log"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// DocumentHandler transitions document status through the processing lifecycle.
// The actual chunk/embed logic is a no-op placeholder injected in the chunking
// & embedding milestone; this handler owns the state machine and plumbing.
type DocumentHandler struct {
	docs document.Repository
}

// NewDocumentHandler creates a DocumentHandler backed by docs.
func NewDocumentHandler(docs document.Repository) *DocumentHandler {
	return &DocumentHandler{docs: docs}
}

// Handle transitions the document from pending → processing → indexed.
// If the document is already indexed the call is a no-op (idempotent).
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

	// Chunk and embedding logic is added in the chunking & embedding milestone.

	if err := h.docs.UpdateStatus(ctx, job.UserID, job.DocumentID, document.StatusIndexed); err != nil {
		return fmt.Errorf("dochandler: mark indexed: %w", err)
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
