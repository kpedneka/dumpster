package kb_test

import (
	"context"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/kb/memory"
)

var _ kb.Repository = (*memory.Repository)(nil)

func TestKB_CRUD(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()

	created, err := repo.Create(ctx, 1, "my kb")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.UserID != 1 || created.Name != "my kb" {
		t.Fatalf("unexpected created: %+v", created)
	}

	got, err := repo.Get(ctx, 1, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID {
		t.Fatalf("get: id mismatch")
	}

	list, err := repo.List(ctx, 1)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: got %d items, want 1", len(list))
	}

	if err := repo.Delete(ctx, 1, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, 1, created.ID); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestKB_TenantIsolation(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()

	kb1, _ := repo.Create(ctx, 1, "user1 kb")

	// user 2 must not see user 1's KB
	if _, err := repo.Get(ctx, 2, kb1.ID); err == nil {
		t.Fatal("user2 should not be able to Get user1's KB")
	}

	// user 2's list must be empty
	list, _ := repo.List(ctx, 2)
	if len(list) != 0 {
		t.Fatalf("user2 List should be empty, got %d", len(list))
	}

	// user 2 must not be able to delete user 1's KB
	if err := repo.Delete(ctx, 2, kb1.ID); err == nil {
		t.Fatal("user2 should not be able to Delete user1's KB")
	}

	// user 1 can still access their own KB after user 2's failed attempts
	if _, err := repo.Get(ctx, 1, kb1.ID); err != nil {
		t.Fatalf("user1 should still have access: %v", err)
	}
}
