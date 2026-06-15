package memory_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/chunk/memory"
)

func makeChunk(userID, kbID, docID uuid.UUID, ordinal int, text string) *chunk.Chunk {
	return &chunk.Chunk{
		DocumentID: docID,
		KBID:       kbID,
		UserID:     userID,
		Ordinal:    ordinal,
		Text:       text,
	}
}

func TestBulkCreate(t *testing.T) {
	r := memory.New()
	userID, kbID, docID := uuid.New(), uuid.New(), uuid.New()

	chunks := []*chunk.Chunk{
		makeChunk(userID, kbID, docID, 0, "first"),
		makeChunk(userID, kbID, docID, 1, "second"),
	}
	if err := r.BulkCreate(context.Background(), chunks); err != nil {
		t.Fatal(err)
	}

	got, err := r.ListByDocument(context.Background(), userID, docID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("count: got %d, want 2", len(got))
	}
	if got[0].ID == uuid.Nil {
		t.Error("BulkCreate should assign IDs")
	}
}

func TestListByDocument_Empty(t *testing.T) {
	r := memory.New()
	got, err := r.ListByDocument(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("count: got %d, want 0", len(got))
	}
}

func TestListByKB(t *testing.T) {
	r := memory.New()
	userID, kbID := uuid.New(), uuid.New()
	doc1, doc2 := uuid.New(), uuid.New()

	_ = r.BulkCreate(context.Background(), []*chunk.Chunk{
		makeChunk(userID, kbID, doc1, 0, "a"),
		makeChunk(userID, kbID, doc2, 0, "b"),
		makeChunk(userID, uuid.New(), uuid.New(), 0, "other kb"),
	})

	got, err := r.ListByKB(context.Background(), userID, kbID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("count: got %d, want 2", len(got))
	}
}

func TestListByKB_TenantIsolation(t *testing.T) {
	r := memory.New()
	user1, user2 := uuid.New(), uuid.New()
	kbID := uuid.New()

	_ = r.BulkCreate(context.Background(), []*chunk.Chunk{
		makeChunk(user1, kbID, uuid.New(), 0, "user1 chunk"),
	})

	got, err := r.ListByKB(context.Background(), user2, kbID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("cross-tenant: got %d chunks, want 0", len(got))
	}
}

func TestDeleteByDocument(t *testing.T) {
	r := memory.New()
	userID, kbID, docID := uuid.New(), uuid.New(), uuid.New()
	otherDoc := uuid.New()

	_ = r.BulkCreate(context.Background(), []*chunk.Chunk{
		makeChunk(userID, kbID, docID, 0, "to delete"),
		makeChunk(userID, kbID, otherDoc, 0, "keep"),
	})

	if err := r.DeleteByDocument(context.Background(), userID, docID); err != nil {
		t.Fatal(err)
	}

	remaining, _ := r.ListByKB(context.Background(), userID, kbID)
	if len(remaining) != 1 {
		t.Errorf("after delete: got %d, want 1", len(remaining))
	}
	if remaining[0].DocumentID != otherDoc {
		t.Error("wrong chunk was deleted")
	}
}

func TestDeleteByDocument_WrongTenant(t *testing.T) {
	r := memory.New()
	owner, other := uuid.New(), uuid.New()
	kbID, docID := uuid.New(), uuid.New()

	_ = r.BulkCreate(context.Background(), []*chunk.Chunk{
		makeChunk(owner, kbID, docID, 0, "owned"),
	})

	// Delete with wrong tenant — chunk should survive.
	_ = r.DeleteByDocument(context.Background(), other, docID)

	remaining, _ := r.ListByKB(context.Background(), owner, kbID)
	if len(remaining) != 1 {
		t.Errorf("cross-tenant delete: got %d, want 1", len(remaining))
	}
}
