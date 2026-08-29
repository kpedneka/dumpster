package worker

import (
	"context"
	"fmt"
	"log"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/entity"
	"github.com/kunalpednekar/dumpster/internal/graphedge"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// maxEntitiesForEdges bounds how many entities in one chunk co-occurrence
// edges are derived from. Edge count grows as n*(n-1)/2, so a chunk with far
// more "entities" than a normal paragraph would contain is almost always a
// sign of non-prose content — a reference/citation list, where extraction
// finds mostly noise (author initials, dates, journal names) rather than
// meaningful co-occurring concepts — not a richer one. A real chunk like
// this produced 128 entities and 8,128 edges on its own; across one
// document, 63,760 edges, turning a single BulkCreate into a multi-minute
// transaction that held locks blocking unrelated deletes (see the v4.6
// lessons-learned page). Oversized chunks are skipped entirely rather than
// truncated: an arbitrary subset of a reference list's "entities" isn't a
// more meaningful signal, just a smaller amount of noise.
const maxEntitiesForEdges = 25

// EdgeHandler derives co-occurrence edges between entity mentions found
// in the same chunk and persists them to the entity_edges table. It is a
// distinct job stage from EntityHandler: it reads entity rows that entity
// extraction has already produced and never re-extracts or re-embeds, so
// it can be re-run independently.
type EdgeHandler struct {
	docs     document.Repository
	entities entity.Repository
	edges    graphedge.Repository
}

// NewEdgeHandler creates an EdgeHandler wired to the given dependencies.
func NewEdgeHandler(docs document.Repository, entities entity.Repository, edges graphedge.Repository) *EdgeHandler {
	return &EdgeHandler{docs: docs, entities: entities, edges: edges}
}

// Handle derives co-occurrence edges for one document: it lists the
// document's existing entity rows, groups them by chunk, and creates one
// edge per unique (entity_a, entity_b) pair within the same chunk. Pairs
// are canonically ordered (entity_a_id < entity_b_id by UUID string form)
// so the same co-occurrence is always stored the same way regardless of
// extraction order. Replacing rather than appending makes re-running
// idempotent without touching entity or chunk rows.
func (h *EdgeHandler) Handle(ctx context.Context, job *queue.Job) error {
	ctx = auth.WithUserID(ctx, job.UserID)

	doc, err := h.docs.Get(ctx, job.UserID, job.DocumentID)
	if err != nil {
		return fmt.Errorf("edgehandler: get document %s: %w", job.DocumentID, err)
	}

	allEntities, err := h.entities.ListByDocument(ctx, job.UserID, job.DocumentID)
	if err != nil {
		return fmt.Errorf("edgehandler: list entities for document %s: %w", job.DocumentID, err)
	}

	// Group entities by their source chunk.
	byChunk := make(map[string][]*entity.Entity)
	for _, e := range allEntities {
		key := e.ChunkID.String()
		byChunk[key] = append(byChunk[key], e)
	}

	// Generate one edge per unique entity mention pair within each chunk.
	var edges []*graphedge.Edge
	for chunkID, group := range byChunk {
		if len(group) > maxEntitiesForEdges {
			log.Printf("edgehandler: skipping edge derivation for chunk %s (document %s): %d entities exceeds cap of %d, likely non-prose content (e.g. a reference list)",
				chunkID, job.DocumentID, len(group), maxEntitiesForEdges)
			continue
		}
		for i := 0; i < len(group); i++ {
			for j := i + 1; j < len(group); j++ {
				edges = append(edges, graphedge.NewEdge(
					job.DocumentID, doc.KBID, job.UserID,
					group[i].ChunkID, group[i].ID, group[j].ID,
				))
			}
		}
	}

	if err := h.edges.DeleteByDocument(ctx, job.UserID, job.DocumentID); err != nil {
		return fmt.Errorf("edgehandler: clear existing edges: %w", err)
	}

	if len(edges) == 0 {
		return nil
	}

	if err := h.edges.BulkCreate(ctx, edges); err != nil {
		return fmt.Errorf("edgehandler: persist edges: %w", err)
	}

	return nil
}

// OnFailed logs that edge extraction was permanently dead-lettered for the
// document. It deliberately does not touch document.Status: edge extraction
// failures do not affect the document's chunking, embedding, or entity
// status, since this is an independent job stage.
func (h *EdgeHandler) OnFailed(_ context.Context, job *queue.Job) {
	log.Printf("edgehandler: edge extraction permanently failed for document %s", job.DocumentID)
}

var _ Handler = (*EdgeHandler)(nil)
