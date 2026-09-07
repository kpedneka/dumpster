package graphrecall_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/graphrag/memory"
	"github.com/kunalpednekar/dumpster/internal/graphrecall"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
)

func scoredChunk(docID uuid.UUID) retrieval.ScoredChunk {
	return retrieval.ScoredChunk{Chunk: &chunk.Chunk{ID: uuid.New(), DocumentID: docID}}
}

func TestRun_ReachedViaAggregationLeg(t *testing.T) {
	kbID := uuid.New()
	docB := uuid.New()

	store := memory.New()
	store.AggregationChunks = []retrieval.ScoredChunk{scoredChunk(docB)}

	tester := &graphrecall.Tester{Retriever: store, K: 10}
	result, err := tester.Run(context.Background(), kbID, []graphrecall.Pair{
		{ID: "p1", SeedQuery: "Elena Voss", ExpectedDocumentIDs: []uuid.UUID{docB}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Score != 1.0 {
		t.Fatalf("Score = %v, want 1.0", result.Score)
	}
	if len(result.Results) != 1 || !result.Results[0].Reached || result.Results[0].Leg != "aggregation" {
		t.Fatalf("unexpected results: %+v", result.Results)
	}
}

func TestRun_ReachedViaTraversalLegOnly(t *testing.T) {
	kbID := uuid.New()
	docB := uuid.New()
	otherDoc := uuid.New()

	store := memory.New()
	store.AggregationChunks = []retrieval.ScoredChunk{scoredChunk(otherDoc)}
	store.TraversalChunks = []retrieval.ScoredChunk{scoredChunk(docB)}

	tester := &graphrecall.Tester{Retriever: store, K: 10}
	result, err := tester.Run(context.Background(), kbID, []graphrecall.Pair{
		{ID: "p1", SeedQuery: "Elena Voss", ExpectedDocumentIDs: []uuid.UUID{docB}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Score != 1.0 {
		t.Fatalf("Score = %v, want 1.0", result.Score)
	}
	if result.Results[0].Leg != "traversal" {
		t.Fatalf("Leg = %q, want %q", result.Results[0].Leg, "traversal")
	}
}

func TestRun_NotReached(t *testing.T) {
	kbID := uuid.New()
	docB := uuid.New()
	unrelatedDoc := uuid.New()

	store := memory.New()
	store.AggregationChunks = []retrieval.ScoredChunk{scoredChunk(unrelatedDoc)}
	store.TraversalChunks = []retrieval.ScoredChunk{scoredChunk(unrelatedDoc)}

	tester := &graphrecall.Tester{Retriever: store, K: 10}
	result, err := tester.Run(context.Background(), kbID, []graphrecall.Pair{
		{ID: "p1", SeedQuery: "Ines Thackeray", ExpectedDocumentIDs: []uuid.UUID{docB}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Score != 0.0 {
		t.Fatalf("Score = %v, want 0.0", result.Score)
	}
	if result.Results[0].Reached || result.Results[0].Leg != "" {
		t.Fatalf("unexpected result: %+v", result.Results[0])
	}
}

func TestRun_ScoreIsFractionAcrossMultiplePairs(t *testing.T) {
	kbID := uuid.New()
	reachableDoc := uuid.New()
	unreachableTarget := uuid.New()
	unrelatedDoc := uuid.New()

	store := memory.New()
	store.AggregationChunks = []retrieval.ScoredChunk{scoredChunk(reachableDoc), scoredChunk(unrelatedDoc)}
	store.TraversalChunks = []retrieval.ScoredChunk{scoredChunk(unrelatedDoc)}

	tester := &graphrecall.Tester{Retriever: store, K: 10}
	result, err := tester.Run(context.Background(), kbID, []graphrecall.Pair{
		{ID: "reachable", SeedQuery: "seed one", ExpectedDocumentIDs: []uuid.UUID{reachableDoc}},
		{ID: "unreachable", SeedQuery: "seed two", ExpectedDocumentIDs: []uuid.UUID{unreachableTarget}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Score != 0.5 {
		t.Fatalf("Score = %v, want 0.5", result.Score)
	}
}

func TestRun_PairsWithoutExpectedDocumentsAreSkipped(t *testing.T) {
	kbID := uuid.New()
	store := memory.New()

	tester := &graphrecall.Tester{Retriever: store, K: 10}
	result, err := tester.Run(context.Background(), kbID, []graphrecall.Pair{
		{ID: "control", SeedQuery: "Denys Okafor", ExpectedDocumentIDs: nil},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(result.Results) != 0 {
		t.Fatalf("expected control-only pairs to be skipped entirely, got: %+v", result.Results)
	}
}

func TestRun_PropagatesLegError(t *testing.T) {
	kbID := uuid.New()
	store := memory.New()
	store.AggregationErr = errors.New("boom")

	tester := &graphrecall.Tester{Retriever: store, K: 10}
	_, err := tester.Run(context.Background(), kbID, []graphrecall.Pair{
		{ID: "p1", SeedQuery: "x", ExpectedDocumentIDs: []uuid.UUID{uuid.New()}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
