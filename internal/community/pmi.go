package community

import (
	"math"

	"github.com/google/uuid"
)

// minCoOccurrenceForPMI is the smallest raw co-occurrence count PMI trusts
// enough to weight at all. PMI is well known to blow up for a pair that
// co-occurred only once when both entities are individually rare -- a
// single coincidence looks maximally "surprising" by the formula, even
// though one data point proves nothing. A KB with hundreds of near-singleton
// communities (i.e. most raw co-occurrence counts already sitting at 1) is
// exactly the case where this failure mode would otherwise dominate the
// output, so pairs below this floor are dropped rather than assigned an
// inflated weight.
const minCoOccurrenceForPMI = 2

// pmiWeight returns the positive pointwise mutual information (PPMI) of an
// edge between two canonical entities: how much more often they actually
// co-occur than their individual frequencies would predict by chance.
// Negative PMI (co-occurring less than chance would predict) is clipped to
// zero -- on data this sparse, a negative score is noise, not a meaningful
// "these avoid each other" signal, and a caller-facing edge weight has no
// use for it anyway.
//
// totalMentions is a single consistent denominator -- mention counts summed
// across every canonical entity in the KB -- standing in for corpus size.
// There's no natural "number of documents" or "number of chunks" figure
// available at this layer (entity_edges collapses per-mention pairs before
// this point); comparing every pair's ratio against the same total is what
// keeps PMI scores comparable to each other, which matters more here than
// which specific proxy for N is used.
//
// Returns 0 (i.e. the caller should drop the edge) when coOccurrence is
// below minCoOccurrenceForPMI, or a frequency is zero (a defensive
// division-by-zero guard -- canonicalization never actually produces a
// zero-mention canonical entity).
func pmiWeight(coOccurrence float64, freqA, freqB, totalMentions int) float64 {
	if coOccurrence < minCoOccurrenceForPMI || freqA <= 0 || freqB <= 0 || totalMentions <= 0 {
		return 0
	}
	expected := float64(freqA) * float64(freqB) / float64(totalMentions)
	if expected <= 0 {
		return 0
	}
	pmi := math.Log2(coOccurrence / expected)
	if pmi < 0 {
		return 0
	}
	return pmi
}

// ApplyPMIWeighting returns a copy of g with every edge's raw co-occurrence
// weight replaced by its PPMI score (see pmiWeight), computed from freq --
// each node's total mention count across the KB, independent of which other
// nodes it co-occurs with. Edges whose PPMI score comes back 0 (below the
// reliability floor, or no stronger than chance) are dropped entirely
// rather than kept at a zero weight, since Louvain and every degree-based
// consumer downstream should treat "no real association" the same as "no
// edge." g itself is not modified.
//
// This is what actually fixes the "ubiquitous term" problem raw co-occurrence
// counts have: an entity that appears in nearly every chunk of a document
// (a generic technical term, a common pronoun) racks up a high freq, which
// drives its expected co-occurrence with anything else up too -- so its raw
// co-occurrence count with another common entity stops looking surprising,
// and the edge is weighted down or dropped. A pair of individually rare
// entities that co-occur even a few times keeps a high score, since that
// co-occurrence is far more than their low individual frequencies would
// predict by chance.
func ApplyPMIWeighting(g *Graph, freq map[uuid.UUID]int) *Graph {
	var total int
	for _, f := range freq {
		total += f
	}

	out := &Graph{Nodes: g.Nodes}
	for _, e := range g.Edges {
		w := pmiWeight(e.Weight, freq[e.A], freq[e.B], total)
		if w <= 0 {
			continue
		}
		out.Edges = append(out.Edges, WeightedEdge{A: e.A, B: e.B, Weight: w})
	}
	return out
}
