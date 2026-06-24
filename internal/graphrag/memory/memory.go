// Package memory provides an in-memory graphrag.GraphRetriever for tests.
package memory

import (
	"context"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/graphrag"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
)

// Store is a configurable in-memory test double for graphrag.GraphRetriever.
type Store struct {
	AggregationChunks []retrieval.ScoredChunk
	AggregationErr    error
	TraversalChunks   []retrieval.ScoredChunk
	TraversalErr      error
}

// New returns an empty Store (both legs return nil, nil by default).
func New() *Store {
	return &Store{}
}

// AggregationLeg returns the configured AggregationChunks or AggregationErr.
func (s *Store) AggregationLeg(_ context.Context, _ uuid.UUID, _ string, _ int) ([]retrieval.ScoredChunk, error) {
	return s.AggregationChunks, s.AggregationErr
}

// TraversalLeg returns the configured TraversalChunks or TraversalErr.
func (s *Store) TraversalLeg(_ context.Context, _ uuid.UUID, _ string, _ int) ([]retrieval.ScoredChunk, error) {
	return s.TraversalChunks, s.TraversalErr
}

var _ graphrag.GraphRetriever = (*Store)(nil)
