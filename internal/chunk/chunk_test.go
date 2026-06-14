package chunk_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/chunk/memory"
)

var _ chunk.Repository = (*memory.Repository)(nil)

func makeChunks(userID, kbID, documentID uuid.UUID, n int) []*chunk.Chunk {
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
	userID, kbID, docID := uuid.New(), uuid.New(), uuid.New()

	if err := repo.BulkCreate(ctx, makeChunks(userID, kbID, docID, 5)); err != nil {
		t.Fatal(err)
	}

	list, err := repo.ListByDocument(ctx, userID, docID)
	if err != nil || len(list) != 5 {
		t.Fatalf("ListByDocument: got %d, want 5 (err: %v)", len(list), err)
	}

	byKB, err := repo.ListByKB(ctx, userID, kbID)
	if err != nil || len(byKB) != 5 {
		t.Fatalf("ListByKB: got %d, want 5 (err: %v)", len(byKB), err)
	}
}

func TestChunk_DeleteByDocument(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID, kbID := uuid.New(), uuid.New()
	docA, docB := uuid.New(), uuid.New()

	_ = repo.BulkCreate(ctx, makeChunks(userID, kbID, docA, 3))
	_ = repo.BulkCreate(ctx, makeChunks(userID, kbID, docB, 2))

	if err := repo.DeleteByDocument(ctx, userID, docA); err != nil {
		t.Fatal(err)
	}

	remaining, _ := repo.ListByDocument(ctx, userID, docA)
	if len(remaining) != 0 {
		t.Fatalf("expected 0 chunks for docA, got %d", len(remaining))
	}

	untouched, _ := repo.ListByDocument(ctx, userID, docB)
	if len(untouched) != 2 {
		t.Fatalf("expected 2 chunks for docB, got %d", len(untouched))
	}
}

func TestChunk_TenantIsolation(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	user1, user2, kbID, docID := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	_ = repo.BulkCreate(ctx, makeChunks(user1, kbID, docID, 3))

	list, _ := repo.ListByDocument(ctx, user2, docID)
	if len(list) != 0 {
		t.Fatalf("user2 ListByDocument should be empty, got %d", len(list))
	}

	byKB, _ := repo.ListByKB(ctx, user2, kbID)
	if len(byKB) != 0 {
		t.Fatalf("user2 ListByKB should be empty, got %d", len(byKB))
	}

	_ = repo.DeleteByDocument(ctx, user2, docID)
	remaining, _ := repo.ListByDocument(ctx, user1, docID)
	if len(remaining) != 3 {
		t.Fatalf("user1 chunks should be untouched, got %d", len(remaining))
	}
}
