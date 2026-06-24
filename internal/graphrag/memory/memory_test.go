package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/graphrag"
	"github.com/kunalpednekar/dumpster/internal/graphrag/memory"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
)

func scored(c *chunk.Chunk) retrieval.ScoredChunk {
	return retrieval.ScoredChunk{Chunk: c, Score: 1.0}
}

func TestStore_AggregationLeg_ReturnsConfigured(t *testing.T) {
	s := memory.New()
	c := &chunk.Chunk{ID: uuid.New(), Text: "org chunk"}
	s.AggregationChunks = []retrieval.ScoredChunk{scored(c)}

	got, err := s.AggregationLeg(context.Background(), uuid.New(), "FEMA", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != c.ID {
		t.Errorf("unexpected result: %v", got)
	}
}

func TestStore_AggregationLeg_PropagatesError(t *testing.T) {
	s := memory.New()
	s.AggregationErr = errors.New("db down")

	_, err := s.AggregationLeg(context.Background(), uuid.New(), "FEMA", 10)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestStore_TraversalLeg_ReturnsConfigured(t *testing.T) {
	s := memory.New()
	c := &chunk.Chunk{ID: uuid.New(), Text: "hop chunk"}
	s.TraversalChunks = []retrieval.ScoredChunk{scored(c)}

	got, err := s.TraversalLeg(context.Background(), uuid.New(), "q", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != c.ID {
		t.Errorf("unexpected result: %v", got)
	}
}

func TestStore_TraversalLeg_PropagatesError(t *testing.T) {
	s := memory.New()
	s.TraversalErr = errors.New("db down")

	_, err := s.TraversalLeg(context.Background(), uuid.New(), "q", 10)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestStore_DefaultReturnsNilNil(t *testing.T) {
	s := memory.New()

	agg, err := s.AggregationLeg(context.Background(), uuid.New(), "q", 10)
	if err != nil || agg != nil {
		t.Errorf("expected nil, nil; got %v, %v", agg, err)
	}
	trav, err := s.TraversalLeg(context.Background(), uuid.New(), "q", 10)
	if err != nil || trav != nil {
		t.Errorf("expected nil, nil; got %v, %v", trav, err)
	}
}

var _ graphrag.GraphRetriever = (*memory.Store)(nil)
