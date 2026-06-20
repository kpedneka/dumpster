package entity_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/entity"
	"github.com/kunalpednekar/dumpster/internal/entity/memory"
)

var _ entity.Repository = (*memory.Repository)(nil)

func makeEntities(userID, kbID, documentID, chunkID uuid.UUID, n int) []*entity.Entity {
	out := make([]*entity.Entity, n)
	for i := range out {
		out[i] = &entity.Entity{
			DocumentID: documentID,
			KBID:       kbID,
			UserID:     userID,
			ChunkID:    chunkID,
			Type:       entity.Type("person"),
			Text:       "Ada Lovelace",
			Start:      i * 10,
			End:        i*10 + 5,
			Score:      0.9,
		}
	}
	return out
}

func TestEntity_BulkCreateAndList(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID, kbID, docID, chunkID := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	if err := repo.BulkCreate(ctx, makeEntities(userID, kbID, docID, chunkID, 4)); err != nil {
		t.Fatal(err)
	}

	list, err := repo.ListByDocument(ctx, userID, docID)
	if err != nil || len(list) != 4 {
		t.Fatalf("ListByDocument: got %d, want 4 (err: %v)", len(list), err)
	}
	for _, e := range list {
		if e.ID == uuid.Nil {
			t.Error("expected BulkCreate to assign a non-nil ID")
		}
		if e.Type != entity.Type("person") {
			t.Errorf("type: got %q, want %q", e.Type, "person")
		}
	}
}

func TestEntity_DeleteByDocument(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID, kbID, chunkID := uuid.New(), uuid.New(), uuid.New()
	docA, docB := uuid.New(), uuid.New()

	_ = repo.BulkCreate(ctx, makeEntities(userID, kbID, docA, chunkID, 3))
	_ = repo.BulkCreate(ctx, makeEntities(userID, kbID, docB, chunkID, 2))

	if err := repo.DeleteByDocument(ctx, userID, docA); err != nil {
		t.Fatal(err)
	}

	remaining, _ := repo.ListByDocument(ctx, userID, docA)
	if len(remaining) != 0 {
		t.Fatalf("expected 0 entities for docA, got %d", len(remaining))
	}

	untouched, _ := repo.ListByDocument(ctx, userID, docB)
	if len(untouched) != 2 {
		t.Fatalf("expected 2 entities for docB, got %d", len(untouched))
	}
}

func TestEntity_TenantIsolation(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	user1, user2, kbID, docID, chunkID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	_ = repo.BulkCreate(ctx, makeEntities(user1, kbID, docID, chunkID, 3))

	list, _ := repo.ListByDocument(ctx, user2, docID)
	if len(list) != 0 {
		t.Fatalf("user2 ListByDocument should be empty, got %d", len(list))
	}

	_ = repo.DeleteByDocument(ctx, user2, docID)
	remaining, _ := repo.ListByDocument(ctx, user1, docID)
	if len(remaining) != 3 {
		t.Fatalf("user1 entities should be untouched, got %d", len(remaining))
	}
}

func TestEntity_BulkCreate_Empty(t *testing.T) {
	repo := memory.New()
	if err := repo.BulkCreate(context.Background(), nil); err != nil {
		t.Fatalf("BulkCreate(nil) should be a no-op, got error: %v", err)
	}
}
