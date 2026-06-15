// Package retrieval defines the Retriever interface and the ScoredChunk type
// returned by hybrid (vector + keyword) search over a knowledge base.
package retrieval

import (
	"context"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/chunk"
)

// ScoredChunk pairs a chunk with its fused relevance score.
// The chunk carries text, source offsets (CharStart/CharEnd), and a
// document reference (DocumentID) — everything downstream needs for citation.
type ScoredChunk struct {
	*chunk.Chunk
	Score float64
}

// Retriever returns the most-relevant chunks for a natural-language query
// scoped to a single knowledge base and tenant.
type Retriever interface {
	// Retrieve returns at most k ScoredChunks ordered by descending fused score.
	// k must be positive; implementations return an error for k <= 0.
	// The context must carry the authenticated user identity.
	Retrieve(ctx context.Context, kbID uuid.UUID, query string, k int) ([]ScoredChunk, error)
}
