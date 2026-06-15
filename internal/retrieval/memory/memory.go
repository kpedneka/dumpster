// Package memory provides an in-memory retrieval.Retriever for use in tests.
// It delegates chunk storage to a chunk.Repository and performs cosine-similarity
// vector search and substring keyword search locally — no database required.
package memory

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/llm"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
)

// Retriever is an in-memory implementation of retrieval.Retriever that uses
// cosine similarity for the vector leg and case-insensitive substring matching
// for the keyword leg.
type Retriever struct {
	chunks   chunk.Repository
	embedder llm.Embedder
}

// New returns a Retriever backed by the given chunk repository and embedder.
func New(chunks chunk.Repository, embedder llm.Embedder) *Retriever {
	return &Retriever{chunks: chunks, embedder: embedder}
}

// Retrieve embeds query, scores all KB chunks via cosine similarity and
// substring match, then returns the top-k fused results.
func (r *Retriever) Retrieve(ctx context.Context, kbID uuid.UUID, query string, k int) ([]retrieval.ScoredChunk, error) {
	userID, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, errors.New("retrieval: unauthenticated: no user identity in context")
	}

	all, err := r.chunks.ListByKB(ctx, userID, kbID)
	if err != nil {
		return nil, err
	}

	vecs, err := r.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	queryVec := vecs[0]

	vectorRanked := vectorSearch(all, queryVec)
	keywordRanked := keywordSearch(all, query)

	return retrieval.RRF(vectorRanked, keywordRanked, k), nil
}

// vectorSearch ranks chunks by cosine similarity to queryVec, highest first.
// Chunks without an embedding are skipped.
func vectorSearch(all []*chunk.Chunk, queryVec []float32) []retrieval.ScoredChunk {
	type entry struct {
		c   *chunk.Chunk
		sim float64
	}
	var candidates []entry
	for _, c := range all {
		if len(c.Embedding) == 0 || len(queryVec) == 0 {
			continue
		}
		candidates = append(candidates, entry{c, cosineSim(queryVec, c.Embedding)})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].sim > candidates[j].sim })

	out := make([]retrieval.ScoredChunk, len(candidates))
	for i, e := range candidates {
		out[i] = retrieval.ScoredChunk{Chunk: e.c}
	}
	return out
}

// keywordSearch returns chunks whose text contains query (case-insensitive), in
// the order they appear in the store.
func keywordSearch(all []*chunk.Chunk, query string) []retrieval.ScoredChunk {
	lq := strings.ToLower(query)
	var out []retrieval.ScoredChunk
	for _, c := range all {
		if strings.Contains(strings.ToLower(c.Text), lq) {
			out = append(out, retrieval.ScoredChunk{Chunk: c})
		}
	}
	return out
}

// cosineSim computes the cosine similarity between two equal-length vectors.
func cosineSim(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

var _ retrieval.Retriever = (*Retriever)(nil)
