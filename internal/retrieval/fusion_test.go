package retrieval_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
)

func makeChunk(id uuid.UUID, text string) *chunk.Chunk {
	return &chunk.Chunk{ID: id, Text: text}
}

func scored(c *chunk.Chunk) retrieval.ScoredChunk {
	return retrieval.ScoredChunk{Chunk: c}
}

func TestRRF_VectorOnly(t *testing.T) {
	a := makeChunk(uuid.New(), "alpha")
	b := makeChunk(uuid.New(), "beta")

	got := retrieval.RRF(
		[]retrieval.ScoredChunk{scored(a), scored(b)},
		nil,
		10,
	)

	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	// a is rank-1 in vector list → higher RRF score than b
	if got[0].ID != a.ID {
		t.Errorf("want first result %v, got %v", a.ID, got[0].ID)
	}
}

func TestRRF_KeywordOnly(t *testing.T) {
	a := makeChunk(uuid.New(), "alpha")
	b := makeChunk(uuid.New(), "beta")

	got := retrieval.RRF(
		nil,
		[]retrieval.ScoredChunk{scored(a), scored(b)},
		10,
	)

	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if got[0].ID != a.ID {
		t.Errorf("want first result %v, got %v", a.ID, got[0].ID)
	}
}

func TestRRF_FusionBoost(t *testing.T) {
	// a appears in both lists at rank 2; b appears only in vector list at rank 1.
	// Despite b being ranked higher in the vector leg, a's dual-list presence
	// gives it a higher fused score.
	a := makeChunk(uuid.New(), "dual")
	b := makeChunk(uuid.New(), "vector-only")
	c := makeChunk(uuid.New(), "keyword-top")

	vectorRanked := []retrieval.ScoredChunk{scored(b), scored(a)}
	kwRanked := []retrieval.ScoredChunk{scored(c), scored(a)}

	// RRF scores (k=60):
	//   b: 1/61  ≈ 0.01639
	//   a: 1/62 + 1/62 = 2/62 ≈ 0.03226  (appears at rank 2 in both lists)
	//   c: 1/61  ≈ 0.01639

	got := retrieval.RRF(vectorRanked, kwRanked, 10)

	if len(got) != 3 {
		t.Fatalf("want 3 results, got %d", len(got))
	}
	if got[0].ID != a.ID {
		t.Errorf("want fused winner %v (dual-list), got %v", a.ID, got[0].ID)
	}
}

func TestRRF_TopK(t *testing.T) {
	chunks := make([]retrieval.ScoredChunk, 5)
	for i := range chunks {
		chunks[i] = scored(makeChunk(uuid.New(), ""))
	}

	got := retrieval.RRF(chunks, nil, 3)
	if len(got) != 3 {
		t.Errorf("want 3 results after top-k trim, got %d", len(got))
	}
}

func TestRRF_EmptyLists(t *testing.T) {
	got := retrieval.RRF(nil, nil, 10)
	if got == nil {
		t.Error("want non-nil empty slice, got nil")
	}
	if len(got) != 0 {
		t.Errorf("want 0 results, got %d", len(got))
	}
}

func TestRRF_ScoresArePositive(t *testing.T) {
	a := makeChunk(uuid.New(), "test")
	got := retrieval.RRF([]retrieval.ScoredChunk{scored(a)}, nil, 10)
	if len(got) == 0 {
		t.Fatal("expected a result")
	}
	if got[0].Score <= 0 {
		t.Errorf("expected positive RRF score, got %f", got[0].Score)
	}
}
