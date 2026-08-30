package reembed

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	chunkmem "github.com/kunalpednekar/dumpster/internal/chunk/memory"
	kbmem "github.com/kunalpednekar/dumpster/internal/kb/memory"
	"github.com/kunalpednekar/dumpster/internal/llm/mock"
	"github.com/kunalpednekar/dumpster/internal/session"
	sessionmock "github.com/kunalpednekar/dumpster/internal/session/mock"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
)

func seedSession(t *testing.T, sessions *sessionmock.Store) uuid.UUID {
	t.Helper()
	id := uuid.New()
	sessions.Seed(&session.Session{ID: id})
	return id
}

// seedChunk creates a chunk belonging to userID/kbID via BulkCreate and
// returns its assigned ID (chunk/memory.Repository mints its own IDs, so
// the caller-supplied ID on the input struct is not what ends up stored).
func seedChunk(t *testing.T, chunks *chunkmem.Repository, userID, kbID uuid.UUID, text string, embedding []float32) uuid.UUID {
	t.Helper()
	if err := chunks.BulkCreate(context.Background(), []*chunk.Chunk{
		{UserID: userID, KBID: kbID, Text: text, Embedding: embedding},
	}); err != nil {
		t.Fatalf("seed chunk: %v", err)
	}
	rows, err := chunks.ListByKB(context.Background(), userID, kbID)
	if err != nil {
		t.Fatalf("list seeded chunks: %v", err)
	}
	return rows[len(rows)-1].ID
}

func TestRun_NoSessions_ReturnsZeroResult(t *testing.T) {
	r := New(sessionmock.New(), kbmem.New(), chunkmem.New(), mock.NewEmbedder(384))

	result, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reembedded != 0 || len(result.Errors) != 0 {
		t.Errorf("got %+v, want zero result", result)
	}
}

func TestRun_OnlyReembedsChunksWithNilEmbedding(t *testing.T) {
	sessions := sessionmock.New()
	kbs := kbmem.New()
	chunks := chunkmem.New()

	userID := seedSession(t, sessions)
	k, err := kbs.Create(auth.WithUserID(context.Background(), userID), userID, "kb1")
	if err != nil {
		t.Fatal(err)
	}

	pendingID := seedChunk(t, chunks, userID, k.ID, "needs embedding", nil)
	alreadyDoneID := seedChunk(t, chunks, userID, k.ID, "already embedded", []float32{9, 9, 9})

	var embedCalls [][]string
	embedder := mock.NewEmbedder(384)
	embedder.EmbedFn = func(_ context.Context, texts []string) ([][]float32, error) {
		embedCalls = append(embedCalls, texts)
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = []float32{1, 2, 3}
		}
		return out, nil
	}

	r := New(sessions, kbs, chunks, embedder)
	result, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reembedded != 1 || len(result.Errors) != 0 {
		t.Fatalf("got %+v, want Reembedded=1, no errors", result)
	}
	if len(embedCalls) != 1 || len(embedCalls[0]) != 1 || embedCalls[0][0] != "needs embedding" {
		t.Errorf("embed calls: got %+v, want one call for the pending chunk only", embedCalls)
	}

	rows, _ := chunks.ListByKB(auth.WithUserID(context.Background(), userID), userID, k.ID)
	byID := map[uuid.UUID]*chunk.Chunk{}
	for _, c := range rows {
		byID[c.ID] = c
	}
	if got := byID[pendingID].Embedding; len(got) != 3 {
		t.Errorf("pending chunk not re-embedded: got %v", got)
	}
	if got := byID[alreadyDoneID].Embedding; len(got) != 3 || got[0] != 9 {
		t.Errorf("already-embedded chunk should be untouched: got %v", got)
	}
}

func TestRun_BatchesAcrossBatchSize(t *testing.T) {
	sessions := sessionmock.New()
	kbs := kbmem.New()
	chunks := chunkmem.New()

	userID := seedSession(t, sessions)
	k, err := kbs.Create(auth.WithUserID(context.Background(), userID), userID, "kb1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		seedChunk(t, chunks, userID, k.ID, fmt.Sprintf("chunk %d", i), nil)
	}

	var batchSizes []int
	embedder := mock.NewEmbedder(384)
	embedder.EmbedFn = func(_ context.Context, texts []string) ([][]float32, error) {
		batchSizes = append(batchSizes, len(texts))
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = []float32{1}
		}
		return out, nil
	}

	r := New(sessions, kbs, chunks, embedder)
	r.BatchSize = 2

	result, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reembedded != 5 {
		t.Errorf("Reembedded = %d, want 5", result.Reembedded)
	}
	if got := batchSizes; len(got) != 3 || got[0] != 2 || got[1] != 2 || got[2] != 1 {
		t.Errorf("batch sizes: got %v, want [2 2 1]", got)
	}
}

func TestRun_EmbedderErrorForOneBatch_CollectedNotFatal_OthersStillProcess(t *testing.T) {
	sessions := sessionmock.New()
	kbs := kbmem.New()
	chunks := chunkmem.New()

	userA := seedSession(t, sessions)
	kA, err := kbs.Create(auth.WithUserID(context.Background(), userA), userA, "kbA")
	if err != nil {
		t.Fatal(err)
	}
	seedChunk(t, chunks, userA, kA.ID, "will fail to embed", nil)

	userB := seedSession(t, sessions)
	kB, err := kbs.Create(auth.WithUserID(context.Background(), userB), userB, "kbB")
	if err != nil {
		t.Fatal(err)
	}
	seedChunk(t, chunks, userB, kB.ID, "will succeed", nil)

	embedder := mock.NewEmbedder(384)
	embedder.EmbedFn = func(_ context.Context, texts []string) ([][]float32, error) {
		if texts[0] == "will fail to embed" {
			return nil, fmt.Errorf("inference service unreachable")
		}
		return [][]float32{{1, 2}}, nil
	}

	r := New(sessions, kbs, chunks, embedder)
	result, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reembedded != 1 {
		t.Errorf("Reembedded = %d, want 1 (the other tenant's chunk)", result.Reembedded)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %+v, want exactly 1", result.Errors)
	}
}

func TestRun_TenantIsolation_OnlyOwnChunksTouched(t *testing.T) {
	sessions := sessionmock.New()
	kbs := kbmem.New()
	chunks := chunkmem.New()

	userA := seedSession(t, sessions)
	kA, err := kbs.Create(auth.WithUserID(context.Background(), userA), userA, "kbA")
	if err != nil {
		t.Fatal(err)
	}
	seedChunk(t, chunks, userA, kA.ID, "tenant a chunk", nil)

	userB := seedSession(t, sessions)
	kB, err := kbs.Create(auth.WithUserID(context.Background(), userB), userB, "kbB")
	if err != nil {
		t.Fatal(err)
	}
	seedChunk(t, chunks, userB, kB.ID, "tenant b chunk", nil)

	embedder := mock.NewEmbedder(384)
	embedder.EmbedFn = func(_ context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = []float32{1}
		}
		return out, nil
	}

	r := New(sessions, kbs, chunks, embedder)
	result, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reembedded != 2 || len(result.Errors) != 0 {
		t.Fatalf("got %+v, want both tenants' chunks re-embedded with no errors", result)
	}
}
