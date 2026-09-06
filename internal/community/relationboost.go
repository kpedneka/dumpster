package community

import "github.com/google/uuid"

// This package deliberately does not import internal/relation. The
// confirmed/noneFound maps below are built by the caller (pgstore.go, from
// entity_edges.relation_type -- "none" is relation.NoneRelation's literal
// value, restated in that SQL rather than imported, matching how
// GraphView's query already does the same thing) -- this file only ever
// sees the two boolean maps, never the relation_type strings themselves.

const (
	// relationConfirmedBoost multiplies an edge's PMI weight when
	// internal/relation found a real, textually-supported relationship for
	// it -- independent, stronger evidence than co-occurrence statistics
	// alone can ever provide, so it should outweigh a merely-plausible PMI
	// score. 2.0 is a starting point, not an empirically tuned value --
	// worth revisiting once there's a real before/after intrusion-test
	// score to judge it against.
	relationConfirmedBoost = 2.0
	// relationNoneDamp multiplies an edge's PMI weight when
	// internal/relation reviewed the pair's source text and found no
	// clear relationship -- real, if weaker, negative evidence than PMI
	// alone has, since PMI only ever sees co-occurrence counts, never the
	// text itself. Damped rather than dropped: one reviewed mention
	// finding nothing doesn't mean the pair is never related anywhere in
	// the KB, and relation extraction's own incremental design means most
	// edges won't have been reviewed by any given point anyway.
	relationNoneDamp = 0.5
	// relationRescueWeight is the weight given to a confirmed relationship
	// PMI dropped entirely (weight <= 0 -- e.g. a pair that only
	// co-occurred once, below minCoOccurrenceForPMI's floor). This matters
	// in practice, not just in theory: internal/relation only ever reviews
	// pairs that already have a raw co-occurrence edge, and a real fact
	// stated exactly once ("Gerald Combs founded Wireshark") is common and
	// is exactly the kind of clean, single-mention relationship PMI's
	// statistical floor is least equipped to trust -- confirming it
	// through the actual source text is categorically stronger evidence
	// than a co-occurrence count PMI wasn't confident in, so it must
	// survive regardless. A moderate, non-dominant fixed weight (real PMI
	// scores are typically a handful of bits, log2-scaled) rather than an
	// attempt to synthesize a PMI-equivalent score for an edge PMI never
	// trusted enough to keep in the first place.
	relationRescueWeight = 1.0
)

// PairKey returns the lookup key for the edge between a and b, exposed so
// callers building the confirmed/noneFound maps for ApplyRelationBoost use
// the exact same key shape ApplyRelationBoost itself does.
func PairKey(a, b uuid.UUID) [2]uuid.UUID { return [2]uuid.UUID{a, b} }

// ApplyRelationBoost adjusts weighted's edge weights based on what
// internal/relation has found for each one: boosted when it confirmed a
// real relationship, damped when it reviewed the pair and found none, left
// exactly as PMI weighted it when the pair hasn't been reviewed yet (the
// common case, given relation extraction's incremental design). It also
// rescues any confirmed relationship missing from weighted entirely
// because ApplyPMIWeighting dropped it (see relationRescueWeight for why
// this isn't a rare case worth ignoring) -- raw must be the same graph
// weighted was itself computed from, so a "missing" edge here reliably
// means "PMI dropped it," not "it never existed." Runs after
// ApplyPMIWeighting, as a further, independent adjustment -- PMI's own
// math is unaffected either way. Neither input graph is modified.
func ApplyRelationBoost(weighted, raw *Graph, confirmed, noneFound map[[2]uuid.UUID]bool) *Graph {
	out := &Graph{Nodes: weighted.Nodes}
	kept := make(map[[2]uuid.UUID]bool, len(weighted.Edges))
	for _, e := range weighted.Edges {
		key := PairKey(e.A, e.B)
		kept[key] = true
		w := e.Weight
		switch {
		case confirmed[key]:
			w *= relationConfirmedBoost
		case noneFound[key]:
			w *= relationNoneDamp
		}
		out.Edges = append(out.Edges, WeightedEdge{A: e.A, B: e.B, Weight: w})
	}
	for _, e := range raw.Edges {
		key := PairKey(e.A, e.B)
		if kept[key] || !confirmed[key] {
			continue
		}
		out.Edges = append(out.Edges, WeightedEdge{A: e.A, B: e.B, Weight: relationRescueWeight})
	}
	return out
}
