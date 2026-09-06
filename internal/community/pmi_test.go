package community

import (
	"testing"

	"github.com/google/uuid"
)

func TestPMIWeight_DropsBelowMinCoOccurrence(t *testing.T) {
	// A single coincidental co-occurrence between two rare entities would
	// otherwise produce a very high (inflated, unreliable) PMI score.
	if got := pmiWeight(1, 5, 5, 1000); got != 0 {
		t.Errorf("pmiWeight(1, ...) = %v, want 0 (below minCoOccurrenceForPMI)", got)
	}
}

func TestPMIWeight_UbiquitousPairScoresLowOrZero(t *testing.T) {
	// Two entities that are each individually common (freq 500 out of
	// 10000 total mentions -- 5% each) predict an expected co-occurrence of
	// freqA*freqB/total = 25 under independence. Co-occurring 15 times --
	// at or below that chance level -- is exactly the "loop"/"main" hub
	// case from real feedback: high raw co-occurrence counts driven purely
	// by both terms being everywhere, not by any real association.
	got := pmiWeight(15, 500, 500, 10000)
	if got != 0 {
		t.Errorf("pmiWeight for a below-chance co-occurrence = %v, want 0", got)
	}
}

func TestPMIWeight_RarePairThatCoOccursScoresHigh(t *testing.T) {
	// Two individually rare entities (low freq) that co-occur even a
	// handful of times is far more than chance predicts -- this is the
	// "daytab"/"multi-dimensional arrays" case from real feedback.
	rare := pmiWeight(4, 5, 6, 1000)
	ubiquitous := pmiWeight(400, 500, 500, 1000)
	if rare <= ubiquitous {
		t.Errorf("rare-pair PMI (%v) should score higher than ubiquitous-pair PMI (%v)", rare, ubiquitous)
	}
	if rare <= 0 {
		t.Errorf("pmiWeight for a genuinely surprising co-occurrence = %v, want > 0", rare)
	}
}

func TestPMIWeight_NegativeScoreClippedToZero(t *testing.T) {
	// Co-occurring less than chance would predict is clipped to 0 (PPMI),
	// not returned as a negative weight -- see pmiWeight's doc comment for
	// why a negative score isn't a meaningful signal here.
	got := pmiWeight(2, 900, 900, 1000)
	if got != 0 {
		t.Errorf("pmiWeight for a below-chance pair = %v, want 0 (clipped)", got)
	}
}

func TestPMIWeight_ZeroFrequencyGuardsAgainstDivideByZero(t *testing.T) {
	if got := pmiWeight(5, 0, 5, 1000); got != 0 {
		t.Errorf("pmiWeight with freqA=0 = %v, want 0", got)
	}
	if got := pmiWeight(5, 5, 0, 1000); got != 0 {
		t.Errorf("pmiWeight with freqB=0 = %v, want 0", got)
	}
	if got := pmiWeight(5, 5, 5, 0); got != 0 {
		t.Errorf("pmiWeight with totalMentions=0 = %v, want 0", got)
	}
}

func TestApplyPMIWeighting_DropsLowScoringEdgesKeepsHighScoringOnes(t *testing.T) {
	loop, main, daytab, arrays := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	g := &Graph{
		Nodes: []uuid.UUID{loop, main, daytab, arrays},
		Edges: []WeightedEdge{
			{A: loop, B: main, Weight: 200},   // ubiquitous pair, high raw weight, but at/below chance level given both freqs
			{A: daytab, B: arrays, Weight: 4}, // rare pair, low raw weight, far above chance level
		},
	}
	freq := map[uuid.UUID]int{loop: 500, main: 500, daytab: 5, arrays: 6}

	out := ApplyPMIWeighting(g, freq)

	if len(out.Edges) != 1 {
		t.Fatalf("got %d edges, want 1 (the ubiquitous pair should be dropped)", len(out.Edges))
	}
	got := out.Edges[0]
	isDaytabArraysPair := (got.A == daytab && got.B == arrays) || (got.A == arrays && got.B == daytab)
	if !isDaytabArraysPair {
		t.Errorf("surviving edge = %+v, want the daytab/arrays pair", got)
	}
	if got.Weight <= 0 {
		t.Errorf("surviving edge weight = %v, want > 0", got.Weight)
	}
}

func TestApplyPMIWeighting_PreservesNodesEvenWithNoSurvivingEdges(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	g := &Graph{
		Nodes: []uuid.UUID{a, b},
		Edges: []WeightedEdge{{A: a, B: b, Weight: 1}}, // below minCoOccurrenceForPMI
	}
	freq := map[uuid.UUID]int{a: 10, b: 10}

	out := ApplyPMIWeighting(g, freq)

	if len(out.Nodes) != 2 {
		t.Errorf("got %d nodes, want 2 (nodes must survive even with all edges dropped)", len(out.Nodes))
	}
	if len(out.Edges) != 0 {
		t.Errorf("got %d edges, want 0", len(out.Edges))
	}
}

func TestApplyPMIWeighting_DoesNotMutateInput(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	g := &Graph{
		Nodes: []uuid.UUID{a, b},
		Edges: []WeightedEdge{{A: a, B: b, Weight: 4}},
	}
	freq := map[uuid.UUID]int{a: 5, b: 6}

	ApplyPMIWeighting(g, freq)

	if g.Edges[0].Weight != 4 {
		t.Errorf("input graph was mutated: edge weight = %v, want unchanged 4", g.Edges[0].Weight)
	}
}
