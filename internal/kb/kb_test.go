package kb_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/kb/memory"
)

var _ kb.Repository = (*memory.Repository)(nil)

func TestKB_CRUD(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID := uuid.New()

	created, err := repo.Create(ctx, userID, "my kb")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == uuid.Nil || created.UserID != userID || created.Name != "my kb" {
		t.Fatalf("unexpected created: %+v", created)
	}

	got, err := repo.Get(ctx, userID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID {
		t.Fatalf("get: id mismatch")
	}

	list, err := repo.List(ctx, userID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: got %d items, want 1", len(list))
	}

	if err := repo.Delete(ctx, userID, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, userID, created.ID); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestKB_Rename(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID := uuid.New()

	created, _ := repo.Create(ctx, userID, "original")

	renamed, err := repo.Rename(ctx, userID, created.ID, "updated")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "updated" {
		t.Fatalf("name: got %q, want %q", renamed.Name, "updated")
	}
	if renamed.UpdatedAt.Before(created.CreatedAt) {
		t.Fatal("updated_at should be >= created_at")
	}

	// Cross-tenant rename should fail.
	other := uuid.New()
	if _, err := repo.Rename(ctx, other, created.ID, "hijack"); err == nil {
		t.Fatal("other user should not be able to rename")
	}
}

func TestKB_TenantIsolation(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	user1, user2 := uuid.New(), uuid.New()

	kb1, _ := repo.Create(ctx, user1, "user1 kb")

	if _, err := repo.Get(ctx, user2, kb1.ID); err == nil {
		t.Fatal("user2 should not be able to Get user1's KB")
	}

	list, _ := repo.List(ctx, user2)
	if len(list) != 0 {
		t.Fatalf("user2 List should be empty, got %d", len(list))
	}

	if err := repo.Delete(ctx, user2, kb1.ID); err == nil {
		t.Fatal("user2 should not be able to Delete user1's KB")
	}

	if _, err := repo.Get(ctx, user1, kb1.ID); err != nil {
		t.Fatalf("user1 should still have access: %v", err)
	}
}
