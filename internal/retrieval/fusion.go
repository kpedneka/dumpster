package retrieval

import (
	"sort"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/chunk"
)

// rrfK is the standard Reciprocal Rank Fusion smoothing constant.
// k=60 is the value from the original RRF paper (Cormack et al., 2009).
const rrfK = 60.0

// RRFMerge merges any number of ranked candidate lists using Reciprocal Rank
// Fusion and returns the top-k results. It is the variadic generalisation used
// when the number of retrieval legs is not fixed at two (e.g. hybrid + graph).
//
// Each chunk's score is the sum of 1/(rrfK+rank) across every list it appears
// in, so a chunk appearing highly in multiple legs outscores one that dominates
// only one leg. A chunk absent from a list contributes nothing from that leg.
func RRFMerge(k int, lists ...[]ScoredChunk) []ScoredChunk {
	scores := make(map[uuid.UUID]float64)
	byID := make(map[uuid.UUID]*chunk.Chunk)

	for _, list := range lists {
		for i, sc := range list {
			scores[sc.ID] += 1.0 / (rrfK + float64(i+1))
			byID[sc.ID] = sc.Chunk
		}
	}

	out := make([]ScoredChunk, 0, len(scores))
	for id, score := range scores {
		out = append(out, ScoredChunk{Chunk: byID[id], Score: score})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })

	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}

// RRF merges two ranked candidate lists — vectorRanked (ANN order) and
// keywordRanked (BM25/ts_rank order) — into a single top-k result.
// It is a convenience wrapper around RRFMerge for the common two-leg case.
func RRF(vectorRanked, keywordRanked []ScoredChunk, k int) []ScoredChunk {
	return RRFMerge(k, vectorRanked, keywordRanked)
}
