package worker

import (
	"fmt"

	"context"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/entity"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// EntityHandler runs local entity extraction over a document's existing
// chunks. It is a distinct job stage from DocumentHandler: it reads chunks
// that chunking/embedding has already produced and never re-chunks or
// re-embeds, so it can be re-run independently — e.g. after the configured
// entity type set changes — without re-touching the document's vector or
// keyword representation.
type EntityHandler struct {
	docs      document.Repository
	chunks    chunk.Repository
	entities  entity.Repository
	extractor entity.Extractor
	// allowedTypes is the config-driven, domain-agnostic entity type set
	// (see internal/config ENTITY_TYPES). Adding a type is a config change,
	// not a code change, and requires no migration.
	allowedTypes []entity.Type
}

// NewEntityHandler creates an EntityHandler wired to the given dependencies.
// allowedTypes is the entity type set read from config.
func NewEntityHandler(
	docs document.Repository,
	chunks chunk.Repository,
	entities entity.Repository,
	extractor entity.Extractor,
	allowedTypes []string,
) *EntityHandler {
	types := make([]entity.Type, len(allowedTypes))
	for i, t := range allowedTypes {
		types[i] = entity.Type(t)
	}
	return &EntityHandler{
		docs:         docs,
		chunks:       chunks,
		entities:     entities,
		extractor:    extractor,
		allowedTypes: types,
	}
}

// Handle runs entity extraction for one document job: it fetches the
// document's existing chunks (without re-chunking or re-embedding them),
// runs the configured Extractor over them, and replaces any previously
// stored entities for that document. Replacing rather than appending makes
// re-running extraction on an already-processed document idempotent.
func (h *EntityHandler) Handle(ctx context.Context, job *queue.Job) error {
	ctx = auth.WithUserID(ctx, job.UserID)

	// Confirm the document exists and belongs to this tenant before doing
	// any work; mirrors DocumentHandler's tenant-scoped lookup.
	if _, err := h.docs.Get(ctx, job.UserID, job.DocumentID); err != nil {
		return fmt.Errorf("entityhandler: get document %s: %w", job.DocumentID, err)
	}

	chunks, err := h.chunks.ListByDocument(ctx, job.UserID, job.DocumentID)
	if err != nil {
		return fmt.Errorf("entityhandler: list chunks for document %s: %w", job.DocumentID, err)
	}
	if len(chunks) == 0 {
		// No chunks yet (document indexing hasn't run, or produced none);
		// nothing to extract from. Not an error — chunking is a separate,
		// upstream job stage this handler does not trigger.
		return nil
	}

	entities, err := h.extractor.Extract(ctx, chunks, h.allowedTypes)
	if err != nil {
		return fmt.Errorf("entityhandler: extract: %w", err)
	}

	// Delete any entities from a previous run before persisting the new
	// set, so re-running extraction (e.g. after a type-set change) is
	// idempotent and doesn't leave stale rows from the old type set
	// around. This only touches the entities table — chunks and their
	// embeddings are never modified here.
	if err := h.entities.DeleteByDocument(ctx, job.UserID, job.DocumentID); err != nil {
		return fmt.Errorf("entityhandler: clear existing entities: %w", err)
	}

	if len(entities) == 0 {
		return nil
	}

	// The extractor is not required to populate UserID; backfill from the
	// job so persisted rows are always correctly tenant-scoped regardless
	// of the Extractor implementation.
	for _, e := range entities {
		e.UserID = job.UserID
	}

	if err := h.entities.BulkCreate(ctx, entities); err != nil {
		return fmt.Errorf("entityhandler: persist entities: %w", err)
	}

	return nil
}

// OnFailed is a no-op: entity extraction failures do not affect the
// document's chunking/embedding status, since this is an independent job
// stage. The job will simply remain unextracted until retried.
func (h *EntityHandler) OnFailed(_ context.Context, job *queue.Job) {
	_ = job
}

var _ Handler = (*EntityHandler)(nil)
