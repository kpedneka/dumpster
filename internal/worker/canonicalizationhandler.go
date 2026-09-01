package worker

import (
	"context"
	"fmt"
	"log"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/canonical"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/entity"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// CanonicalizationHandler resolves a document's entity mentions to stable
// canonical entities (internal/canonical), giving query-time graph
// traversal a cross-chunk, cross-document identity to hop on. It is a
// distinct job stage from EdgeHandler: it reads entity rows that entity
// extraction has already produced and never re-extracts or touches
// entity_edges, so it can be re-run independently.
type CanonicalizationHandler struct {
	docs      document.Repository
	entities  entity.Repository
	canonical canonical.Repository
}

// NewCanonicalizationHandler creates a CanonicalizationHandler wired to the
// given dependencies.
func NewCanonicalizationHandler(docs document.Repository, entities entity.Repository, canonicalRepo canonical.Repository) *CanonicalizationHandler {
	return &CanonicalizationHandler{docs: docs, entities: entities, canonical: canonicalRepo}
}

// Handle resolves canonical identities for one document's entity mentions.
// It only processes mentions not already linked to a canonical entity (see
// canonical.ResolveNew), so a redelivered job — e.g. after this handler
// succeeded but the queue never saw the ack — does not double-count.
func (h *CanonicalizationHandler) Handle(ctx context.Context, job *queue.Job) error {
	ctx = auth.WithUserID(ctx, job.UserID)

	if _, err := h.docs.Get(ctx, job.UserID, job.DocumentID); err != nil {
		return fmt.Errorf("canonicalizationhandler: get document %s: %w", job.DocumentID, err)
	}

	mentions, err := h.entities.ListByDocument(ctx, job.UserID, job.DocumentID)
	if err != nil {
		return fmt.Errorf("canonicalizationhandler: list entities for document %s: %w", job.DocumentID, err)
	}
	if len(mentions) == 0 {
		return nil
	}

	if err := canonical.ResolveNew(ctx, h.canonical, h.entities, job.UserID, mentions); err != nil {
		return fmt.Errorf("canonicalizationhandler: resolve document %s: %w", job.DocumentID, err)
	}

	return nil
}

// OnFailed logs that canonicalization was permanently dead-lettered for the
// document. It deliberately does not touch document.Status: canonicalization
// failures do not affect the document's chunking, embedding, or entity
// status, since this is an independent job stage.
func (h *CanonicalizationHandler) OnFailed(_ context.Context, job *queue.Job) {
	log.Printf("canonicalizationhandler: canonicalization permanently failed for document %s", job.DocumentID)
}

var _ Handler = (*CanonicalizationHandler)(nil)
