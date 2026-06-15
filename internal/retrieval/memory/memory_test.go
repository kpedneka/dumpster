package memory_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	chunkmem "github.com/kunalpednekar/dumpster/internal/chunk/memory"
	"github.com/kunalpednekar/dumpster/internal/llm/mock"
	"github.com/kunalpednekar/dumpster/internal/retrieval/memory"
)

const dims = 4

func userCtx() (context.Context, uuid.UUID) {
	uid := uuid.New()
	return auth.WithUserID(context.Background(), uid), uid
}

// seed adds chunks to the repository and returns them.
func seed(t *testing.T, repo *chunkmem.Repository, kbID, userID uuid.UUID, texts []string, embeddings [][]float32) []*chunk.Chunk {
	t.Helper()
	chunks := make([]*chunk.Chunk, len(texts))
	for i, text := range texts {
		c := &chunk.Chunk{
			KBID:   kbID,
			UserID: userID,
			Text:   text,
		}
		if embeddings != nil {
			c.Embedding = embeddings[i]
		}
		chunks[i] = c
	}
	ctx := auth.WithUserID(context.Background(), userID)
	if err := repo.BulkCreate(ctx, chunks); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Note: BulkCreate copies each struct internally and assigns the UUID to the copy,
	// not to the original pointer. Returned chunks have ID == uuid.Nil.
	return chunks
}

func TestRetrieve_VectorOnly(t *testing.T) {
	ctx, uid := userCtx()
	kbID := uuid.New()
	repo := chunkmem.New()

	// a is "close" to query (high values in dim 0), b is "far" (high values in dim 1)
	embeddings := [][]float32{
		{1, 0, 0, 0}, // chunk a — close to query
		{0, 1, 0, 0}, // chunk b — orthogonal
	}
	texts := []string{"semantic match", "unrelated text"}
	seed(t, repo, kbID, uid, texts, embeddings)

	// Query embedding is [1,0,0,0] — identical to chunk a
	embedder := mock.NewEmbedder(dims)
	embedder.EmbedFn = func(_ context.Context, _ []string) ([][]float32, error) {
		return [][]float32{{1, 0, 0, 0}}, nil
	}

	r := memory.New(repo, embedder)
	results, err := r.Retrieve(ctx, kbID, "semantic match", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	if results[0].Text != "semantic match" {
		t.Errorf("want top result 'semantic match', got %q", results[0].Text)
	}
}

func TestRetrieve_KeywordOnly(t *testing.T) {
	ctx, uid := userCtx()
	kbID := uuid.New()
	repo := chunkmem.New()

	// No embeddings — keyword leg must surface the right chunk.
	texts := []string{"the quick brown fox", "lorem ipsum dolor"}
	seed(t, repo, kbID, uid, texts, nil)

	// Embedder returns zero vectors — cosine sim is undefined (0); vector leg is empty.
	r := memory.New(repo, mock.NewEmbedder(dims))
	results, err := r.Retrieve(ctx, kbID, "quick brown", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(results) < 1 {
		t.Fatal("want at least 1 result")
	}
	if results[0].Text != "the quick brown fox" {
		t.Errorf("want keyword match first, got %q", results[0].Text)
	}
}

func TestRetrieve_ExactTermSurfacesWeak(t *testing.T) {
	// Verifies acceptance criterion: "an exact-term query surfaces that doc
	// even when semantically weak — the keyword leg earns its place."
	ctx, uid := userCtx()
	kbID := uuid.New()
	repo := chunkmem.New()

	// chunk a: embedding close to query but text has no match
	// chunk b: embedding far from query but text is an exact match
	embeddings := [][]float32{
		{1, 0, 0, 0}, // close semantically
		{0, 0, 0, 1}, // far semantically
	}
	texts := []string{"something unrelated", "ACME-12345 unique identifier"}
	seed(t, repo, kbID, uid, texts, embeddings)

	// Query embedding is [1,0,0,0] — semantically close to chunk a.
	// But query text "ACME-12345" only matches chunk b's keyword leg.
	embedder := mock.NewEmbedder(dims)
	embedder.EmbedFn = func(_ context.Context, _ []string) ([][]float32, error) {
		return [][]float32{{1, 0, 0, 0}}, nil
	}

	r := memory.New(repo, embedder)
	results, err := r.Retrieve(ctx, kbID, "ACME-12345", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	var found bool
	for _, res := range results {
		if res.Text == "ACME-12345 unique identifier" {
			found = true
			break
		}
	}
	if !found {
		t.Error("keyword-only doc should appear in results even when semantically weak")
	}
}

func TestRetrieve_TenantScope(t *testing.T) {
	kbID := uuid.New()
	repo := chunkmem.New()

	// Two tenants, same KB
	ctx1, uid1 := userCtx()
	_, uid2 := userCtx()

	seed(t, repo, kbID, uid1, []string{"tenant one doc"}, nil)
	seed(t, repo, kbID, uid2, []string{"tenant two doc"}, nil)

	r := memory.New(repo, mock.NewEmbedder(dims))

	res1, _ := r.Retrieve(ctx1, kbID, "tenant", 10)
	for _, sc := range res1 {
		if sc.UserID != uid1 {
			t.Errorf("result belongs to wrong tenant: got %v, want %v", sc.UserID, uid1)
		}
	}
}

func TestRetrieve_KBScope(t *testing.T) {
	ctx, uid := userCtx()
	repo := chunkmem.New()

	kb1 := uuid.New()
	kb2 := uuid.New()

	seed(t, repo, kb1, uid, []string{"kb1 doc"}, nil)
	seed(t, repo, kb2, uid, []string{"kb2 doc"}, nil)

	r := memory.New(repo, mock.NewEmbedder(dims))

	res, _ := r.Retrieve(ctx, kb1, "doc", 10)
	for _, sc := range res {
		if sc.KBID != kb1 {
			t.Errorf("result from wrong KB: got %v, want %v", sc.KBID, kb1)
		}
	}
}

func TestRetrieve_TopK(t *testing.T) {
	ctx, uid := userCtx()
	kbID := uuid.New()
	repo := chunkmem.New()

	texts := make([]string, 5)
	for i := range texts {
		texts[i] = "match"
	}
	seed(t, repo, kbID, uid, texts, nil)

	r := memory.New(repo, mock.NewEmbedder(dims))
	results, err := r.Retrieve(ctx, kbID, "match", 3)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("want 3 results (top-k=3), got %d", len(results))
	}
}

func TestRetrieve_Unauthenticated(t *testing.T) {
	repo := chunkmem.New()
	r := memory.New(repo, mock.NewEmbedder(dims))

	_, err := r.Retrieve(context.Background(), uuid.New(), "query", 10)
	if err == nil {
		t.Error("want error for unauthenticated context")
	}
}

func TestRetrieve_InvalidK(t *testing.T) {
	ctx, _ := userCtx()
	r := memory.New(chunkmem.New(), mock.NewEmbedder(dims))

	_, err := r.Retrieve(ctx, uuid.New(), "query", 0)
	if err == nil {
		t.Error("want error for k=0")
	}
	_, err = r.Retrieve(ctx, uuid.New(), "query", -1)
	if err == nil {
		t.Error("want error for k=-1")
	}
}

func TestRetrieve_EmbedderReturnsNoVectors(t *testing.T) {
	ctx, _ := userCtx()
	embedder := mock.NewEmbedder(dims)
	embedder.EmbedFn = func(_ context.Context, _ []string) ([][]float32, error) {
		return [][]float32{}, nil
	}
	r := memory.New(chunkmem.New(), embedder)

	_, err := r.Retrieve(ctx, uuid.New(), "query", 10)
	if err == nil {
		t.Error("want error when embedder returns empty slice")
	}
}

func TestRetrieve_EmbedderReturnsZeroLengthEmbedding(t *testing.T) {
	ctx, _ := userCtx()
	embedder := mock.NewEmbedder(dims)
	embedder.EmbedFn = func(_ context.Context, _ []string) ([][]float32, error) {
		return [][]float32{{}}, nil // non-empty outer, zero-length inner
	}
	r := memory.New(chunkmem.New(), embedder)

	_, err := r.Retrieve(ctx, uuid.New(), "query", 10)
	if err == nil {
		t.Error("want error when embedder returns zero-length embedding")
	}
}

func TestRetrieve_ZeroQueryVectorSkipsVector(t *testing.T) {
	// Zero-norm query vector: cosineSim returns 0 for all chunks (normA==0).
	// Chunks with embeddings should be reached by vectorSearch but score 0,
	// while keyword matches still surface correctly.
	ctx, uid := userCtx()
	kbID := uuid.New()
	repo := chunkmem.New()

	seed(t, repo, kbID, uid, []string{"zero norm test"}, [][]float32{{1, 0, 0, 0}})

	embedder := mock.NewEmbedder(dims)
	embedder.EmbedFn = func(_ context.Context, _ []string) ([][]float32, error) {
		return [][]float32{{0, 0, 0, 0}}, nil // all-zero query vector
	}

	r := memory.New(repo, embedder)
	results, err := r.Retrieve(ctx, kbID, "zero norm test", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	// keyword leg should still surface the match
	if len(results) == 0 {
		t.Error("want at least one result from keyword leg")
	}
}

func TestRetrieve_DimensionMismatch(t *testing.T) {
	ctx, uid := userCtx()
	kbID := uuid.New()
	repo := chunkmem.New()

	// Store chunk with 2-dim embedding; query will have 4-dim embedding.
	// The mismatched chunk should be silently skipped (not panic).
	seed(t, repo, kbID, uid, []string{"mismatch chunk"}, [][]float32{{1, 0}})

	embedder := mock.NewEmbedder(dims)
	embedder.EmbedFn = func(_ context.Context, _ []string) ([][]float32, error) {
		return [][]float32{{1, 0, 0, 0}}, nil // 4-dim query
	}

	r := memory.New(repo, embedder)
	results, err := r.Retrieve(ctx, kbID, "query", 10)
	if err != nil {
		t.Fatalf("want no error for dimension mismatch, got: %v", err)
	}
	// The mismatched chunk should be skipped by the vector leg; no panic.
	for _, res := range results {
		if res.Embedding != nil && len(res.Embedding) != dims {
			t.Errorf("dimension-mismatched chunk leaked into results")
		}
	}
}
