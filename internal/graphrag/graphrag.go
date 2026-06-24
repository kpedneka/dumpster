// Package graphrag provides graph-based retrieval legs that complement the
// hybrid (vector + keyword) search legs. All methods return
// retrieval.ScoredChunk slices — never raw entities or edges — so the
// existing RRF merge mechanism can treat graph legs identically to the hybrid
// legs without special-casing.
package graphrag

import (
	"context"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
)

// GraphRetriever provides chunk-shaped output from the co-occurrence graph
// built during ingestion (the entity_edges table). Implementations query
// that table to surface chunks the hybrid legs structurally cannot reach.
type GraphRetriever interface {
	// AggregationLeg returns the top-k chunks that contain any entity
	// co-occurring with a seed entity whose text appears in query. This
	// answers "what X are mentioned with Y" questions that top-k retrieval
	// can't reliably answer because it truncates before completeness.
	AggregationLeg(ctx context.Context, kbID uuid.UUID, query string, k int) ([]retrieval.ScoredChunk, error)

	// TraversalLeg returns the top-k chunks reachable via two-hop traversal
	// from seed entities whose text appears in query. It discovers chunks
	// that are connected to the query entities through shared co-occurrences
	// but do not themselves mention those entities directly.
	TraversalLeg(ctx context.Context, kbID uuid.UUID, query string, k int) ([]retrieval.ScoredChunk, error)
}
