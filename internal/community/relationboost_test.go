package community

import (
	"testing"

	"github.com/google/uuid"
)

func TestApplyRelationBoost_ConfirmedRelationIncreasesWeight(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	raw := &Graph{Nodes: []uuid.UUID{a, b}, Edges: []WeightedEdge{{A: a, B: b, Weight: 4}}}
	weighted := &Graph{Nodes: raw.Nodes, Edges: []WeightedEdge{{A: a, B: b, Weight: 4}}}
	confirmed := map[[2]uuid.UUID]bool{PairKey(a, b): true}

	out := ApplyRelationBoost(weighted, raw, confirmed, nil)

	if len(out.Edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(out.Edges))
	}
	want := 4 * relationConfirmedBoost
	if out.Edges[0].Weight != want {
		t.Errorf("Weight = %v, want %v", out.Edges[0].Weight, want)
	}
}

func TestApplyRelationBoost_NoneFoundDecreasesWeight(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	raw := &Graph{Nodes: []uuid.UUID{a, b}, Edges: []WeightedEdge{{A: a, B: b, Weight: 4}}}
	weighted := &Graph{Nodes: raw.Nodes, Edges: []WeightedEdge{{A: a, B: b, Weight: 4}}}
	noneFound := map[[2]uuid.UUID]bool{PairKey(a, b): true}

	out := ApplyRelationBoost(weighted, raw, nil, noneFound)

	want := 4 * relationNoneDamp
	if out.Edges[0].Weight != want {
		t.Errorf("Weight = %v, want %v", out.Edges[0].Weight, want)
	}
}

func TestApplyRelationBoost_NotYetReviewedLeavesWeightUnchanged(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	raw := &Graph{Nodes: []uuid.UUID{a, b}, Edges: []WeightedEdge{{A: a, B: b, Weight: 4}}}
	weighted := &Graph{Nodes: raw.Nodes, Edges: []WeightedEdge{{A: a, B: b, Weight: 4}}}

	out := ApplyRelationBoost(weighted, raw, nil, nil)

	if out.Edges[0].Weight != 4 {
		t.Errorf("Weight = %v, want 4 (unreviewed pair untouched)", out.Edges[0].Weight)
	}
}

func TestApplyRelationBoost_ConfirmedTakesPrecedenceOverNoneFound(t *testing.T) {
	// Shouldn't happen in practice (one pair can't be both), but the
	// switch's ordering should still be deterministic and documented by a
	// test rather than left to accident.
	a, b := uuid.New(), uuid.New()
	raw := &Graph{Nodes: []uuid.UUID{a, b}, Edges: []WeightedEdge{{A: a, B: b, Weight: 4}}}
	weighted := &Graph{Nodes: raw.Nodes, Edges: []WeightedEdge{{A: a, B: b, Weight: 4}}}
	key := PairKey(a, b)
	confirmed := map[[2]uuid.UUID]bool{key: true}
	noneFound := map[[2]uuid.UUID]bool{key: true}

	out := ApplyRelationBoost(weighted, raw, confirmed, noneFound)

	want := 4 * relationConfirmedBoost
	if out.Edges[0].Weight != want {
		t.Errorf("Weight = %v, want %v (confirmed should win)", out.Edges[0].Weight, want)
	}
}

func TestApplyRelationBoost_DoesNotMutateInputs(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	raw := &Graph{Nodes: []uuid.UUID{a, b}, Edges: []WeightedEdge{{A: a, B: b, Weight: 4}}}
	weighted := &Graph{Nodes: raw.Nodes, Edges: []WeightedEdge{{A: a, B: b, Weight: 4}}}
	confirmed := map[[2]uuid.UUID]bool{PairKey(a, b): true}

	ApplyRelationBoost(weighted, raw, confirmed, nil)

	if weighted.Edges[0].Weight != 4 {
		t.Errorf("weighted graph was mutated: Weight = %v, want unchanged 4", weighted.Edges[0].Weight)
	}
	if raw.Edges[0].Weight != 4 {
		t.Errorf("raw graph was mutated: Weight = %v, want unchanged 4", raw.Edges[0].Weight)
	}
}

// TestApplyRelationBoost_RescuesConfirmedEdgePMIDropped is the real bug
// this file exists to prevent: a pair confirmed by internal/relation but
// entirely missing from weighted (PMI dropped it, e.g. a single-occurrence
// pair below minCoOccurrenceForPMI) must still make it into the graph
// Louvain sees, since a real fact stated once ("Naomi Reyes founded
// Thistlewood") is common and is exactly the case PMI's statistical floor
// isn't equipped to trust on co-occurrence count alone.
func TestApplyRelationBoost_RescuesConfirmedEdgePMIDropped(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	raw := &Graph{
		Nodes: []uuid.UUID{a, b, c, d},
		Edges: []WeightedEdge{
			{A: a, B: b, Weight: 1}, // confirmed, but PMI drops it below
			{A: c, B: d, Weight: 5}, // survives PMI on its own
		},
	}
	// Simulates ApplyPMIWeighting having already dropped the a-b edge
	// entirely while keeping c-d.
	weighted := &Graph{Nodes: raw.Nodes, Edges: []WeightedEdge{{A: c, B: d, Weight: 3}}}
	confirmed := map[[2]uuid.UUID]bool{PairKey(a, b): true}

	out := ApplyRelationBoost(weighted, raw, confirmed, nil)

	if len(out.Edges) != 2 {
		t.Fatalf("got %d edges, want 2 (c-d kept, a-b rescued)", len(out.Edges))
	}
	var rescued *WeightedEdge
	for i, e := range out.Edges {
		if e.A == a && e.B == b {
			rescued = &out.Edges[i]
		}
	}
	if rescued == nil {
		t.Fatal("confirmed a-b edge was not rescued")
	}
	if rescued.Weight != relationRescueWeight {
		t.Errorf("rescued edge Weight = %v, want %v", rescued.Weight, relationRescueWeight)
	}
}

func TestApplyRelationBoost_DoesNotRescueUnconfirmedDroppedEdge(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	raw := &Graph{Nodes: []uuid.UUID{a, b}, Edges: []WeightedEdge{{A: a, B: b, Weight: 1}}}
	weighted := &Graph{Nodes: raw.Nodes} // PMI dropped the only edge, nothing confirmed

	out := ApplyRelationBoost(weighted, raw, nil, nil)

	if len(out.Edges) != 0 {
		t.Errorf("got %d edges, want 0 (unconfirmed dropped edge stays dropped)", len(out.Edges))
	}
}
