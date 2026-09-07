// Package graphrecall measures whether the entity co-occurrence graph
// (entity_edges / canonical_entities) actually connects entities that a
// person would recognize as related but that never co-occur in the same
// chunk -- the retrieval-facing counterpart to the intrusion test, which
// only ever scores community/theme quality, a code path retrieval never
// touches.
//
// This package exercises graphrag.GraphRetriever directly -- the same
// interface AggregationLeg and TraversalLeg satisfy in production -- rather
// than reimplementing a parallel graph search, so a passing score here
// means production retrieval would actually find the connection, not that
// an idealized graph search could find it in principle.
package graphrecall

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/graphrag"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
)

// Pair is one known relationship the graph should be able to surface even
// though its two entities never co-occur in the same chunk.
type Pair struct {
	// ID identifies this pair for reporting, stable across runs so
	// before/after comparisons can be made per-pair, not just in aggregate.
	ID string
	// SeedQuery is the text used to seed graph retrieval -- normally the
	// first entity's canonical name, since both graphrag legs match seeds
	// by substring against canonical_entities.normalized_text.
	SeedQuery string
	// ExpectedDocumentIDs are the documents that should appear among the
	// retrieved chunks if the graph actually connects SeedQuery to the
	// related entity this pair describes. A pair with no entries here is
	// skipped by Run -- it isn't a graph-recall test (e.g. a same-topic
	// control question with nothing cross-document to find).
	ExpectedDocumentIDs []uuid.UUID
}

// PairResult is one pair's outcome.
type PairResult struct {
	PairID  string
	Reached bool
	// Leg records which leg surfaced an expected document first --
	// "aggregation", "traversal", or "" if neither did.
	Leg string
}

// Result is one run's aggregate outcome.
type Result struct {
	// Score is the fraction of testable pairs reached, in [0,1]. Zero
	// (with no Results) when every pair was skipped for having no expected
	// documents -- not a failure, just nothing measurable was passed in.
	Score   float64
	Results []PairResult
}

// Tester runs the graph-recall check against a graphrag.GraphRetriever.
type Tester struct {
	Retriever graphrag.GraphRetriever
	// K bounds how many chunks each leg is asked for. Should match
	// whatever k production search actually uses, so this measures what a
	// real query would surface rather than an artificially generous
	// retrieval budget.
	K int
}

// Run checks each pair by calling both graph legs with SeedQuery and
// testing whether any returned chunk belongs to one of
// ExpectedDocumentIDs. Pairs with no ExpectedDocumentIDs are dropped
// before scoring.
func (t *Tester) Run(ctx context.Context, kbID uuid.UUID, pairs []Pair) (Result, error) {
	testable := make([]Pair, 0, len(pairs))
	for _, p := range pairs {
		if len(p.ExpectedDocumentIDs) > 0 {
			testable = append(testable, p)
		}
	}
	if len(testable) == 0 {
		return Result{}, nil
	}

	results := make([]PairResult, 0, len(testable))
	var reached int
	for _, p := range testable {
		leg, ok, err := t.checkPair(ctx, kbID, p)
		if err != nil {
			return Result{}, fmt.Errorf("graphrecall: pair %s: %w", p.ID, err)
		}
		if ok {
			reached++
		}
		results = append(results, PairResult{PairID: p.ID, Reached: ok, Leg: leg})
	}

	return Result{
		Score:   float64(reached) / float64(len(testable)),
		Results: results,
	}, nil
}

func (t *Tester) checkPair(ctx context.Context, kbID uuid.UUID, p Pair) (string, bool, error) {
	aggChunks, err := t.Retriever.AggregationLeg(ctx, kbID, p.SeedQuery, t.K)
	if err != nil {
		return "", false, fmt.Errorf("aggregation leg: %w", err)
	}
	if containsExpectedDocument(aggChunks, p.ExpectedDocumentIDs) {
		return "aggregation", true, nil
	}

	travChunks, err := t.Retriever.TraversalLeg(ctx, kbID, p.SeedQuery, t.K)
	if err != nil {
		return "", false, fmt.Errorf("traversal leg: %w", err)
	}
	if containsExpectedDocument(travChunks, p.ExpectedDocumentIDs) {
		return "traversal", true, nil
	}

	return "", false, nil
}

func containsExpectedDocument(chunks []retrieval.ScoredChunk, expected []uuid.UUID) bool {
	want := make(map[uuid.UUID]struct{}, len(expected))
	for _, id := range expected {
		want[id] = struct{}{}
	}
	for _, c := range chunks {
		if _, ok := want[c.DocumentID]; ok {
			return true
		}
	}
	return false
}
