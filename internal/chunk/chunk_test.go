package chunk_test

import (
	"context"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/chunk/memory"
)

var _ chunk.Repository = (*memory.Repository)(nil)

func makeChunks(userID, kbID, documentID int64, n int) []*chunk.Chunk {
	out := make([]*chunk.Chunk, n)
	for i := range out {
		out[i] = &chunk.Chunk{
			DocumentID: documentID,
			KBID:       kbID,
			UserID:     userID,
			Ordinal:    i,
			Text:       "text segment",
			TokenCount: 10,
			CharStart:  i * 100,
			CharEnd:    i*100 + 99,
		}
	}
	return out
}

func TestChunk_BulkCreateAndList(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()

	if err := repo.BulkCreate(ctx, makeChunks(1, 10, 100, 5)); err != nil {
		t.Fatal(err)
	}

	list, err := repo.ListByDocument(ctx, 1, 100)
	if err != nil || len(list) != 5 {
		t.Fatalf("ListByDocument: got %d, want 5 (err: %v)", len(list), err)
	}

	byKB, err := repo.ListByKB(ctx, 1, 10)
	if err != nil || len(byKB) != 5 {
		t.Fatalf("ListByKB: got %d, want 5 (err: %v)", len(byKB), err)
	}
}

func TestChunk_DeleteByDocument(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()

	_ = repo.BulkCreate(ctx, makeChunks(1, 10, 100, 3))
	_ = repo.BulkCreate(ctx, makeChunks(1, 10, 200, 2))

	if err := repo.DeleteByDocument(ctx, 1, 100); err != nil {
		t.Fatal(err)
	}

	remaining, _ := repo.ListByDocument(ctx, 1, 100)
	if len(remaining) != 0 {
		t.Fatalf("expected 0 chunks for doc 100, got %d", len(remaining))
	}

	untouched, _ := repo.ListByDocument(ctx, 1, 200)
	if len(untouched) != 2 {
		t.Fatalf("expected 2 chunks for doc 200, got %d", len(untouched))
	}
}

func TestChunk_TenantIsolation(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()

	_ = repo.BulkCreate(ctx, makeChunks(1, 10, 100, 3))

	list, _ := repo.ListByDocument(ctx, 2, 100)
	if len(list) != 0 {
		t.Fatalf("user2 ListByDocument should be empty, got %d", len(list))
	}

	byKB, _ := repo.ListByKB(ctx, 2, 10)
	if len(byKB) != 0 {
		t.Fatalf("user2 ListByKB should be empty, got %d", len(byKB))
	}

	// delete by user2 should silently no-op (not error, not delete user1's data)
	_ = repo.DeleteByDocument(ctx, 2, 100)
	remaining, _ := repo.ListByDocument(ctx, 1, 100)
	if len(remaining) != 3 {
		t.Fatalf("user1 chunks should be untouched, got %d", len(remaining))
	}
}
