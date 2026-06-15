package retrieval

import (
	"sort"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/chunk"
)

// rrfK is the standard Reciprocal Rank Fusion smoothing constant.
// k=60 is the value from the original RRF paper (Cormack et al., 2009).
const rrfK = 60.0

// RRF merges two ranked candidate lists — vectorRanked (ANN order) and
// keywordRanked (BM25/ts_rank order) — into a single top-k result using
// Reciprocal Rank Fusion.
//
// Each chunk's RRF score is the sum of 1/(k+rank) across the lists it appears
// in, so a chunk that ranks highly in both lists outscores one that dominates
// only one leg. A chunk absent from a list contributes nothing from that leg.
func RRF(vectorRanked, keywordRanked []ScoredChunk, k int) []ScoredChunk {
	scores := make(map[uuid.UUID]float64)
	byID := make(map[uuid.UUID]*chunk.Chunk)

	addList := func(list []ScoredChunk) {
		for i, sc := range list {
			scores[sc.ID] += 1.0 / (rrfK + float64(i+1))
			byID[sc.ID] = sc.Chunk
		}
	}
	addList(vectorRanked)
	addList(keywordRanked)

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
