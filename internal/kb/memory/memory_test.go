package memory_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/kb/memory"
)

func TestCreate(t *testing.T) {
	r := memory.New()
	userID := uuid.New()

	k, err := r.Create(context.Background(), userID, "my kb")
	if err != nil {
		t.Fatal(err)
	}
	if k.ID == uuid.Nil {
		t.Error("expected non-nil ID")
	}
	if k.Name != "my kb" {
		t.Errorf("name: got %q, want %q", k.Name, "my kb")
	}
	if k.UserID != userID {
		t.Errorf("user_id mismatch")
	}
}

func TestGet(t *testing.T) {
	r := memory.New()
	userID := uuid.New()
	created, _ := r.Create(context.Background(), userID, "kb")

	got, err := r.Get(context.Background(), userID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID {
		t.Errorf("id mismatch")
	}
}

func TestGet_NotFound(t *testing.T) {
	r := memory.New()
	userID := uuid.New()

	_, err := r.Get(context.Background(), userID, uuid.New())
	if err != kb.ErrNotFound {
		t.Errorf("got %v, want kb.ErrNotFound", err)
	}
}

func TestGet_WrongTenant(t *testing.T) {
	r := memory.New()
	owner := uuid.New()
	other := uuid.New()
	created, _ := r.Create(context.Background(), owner, "kb")

	_, err := r.Get(context.Background(), other, created.ID)
	if err != kb.ErrNotFound {
		t.Errorf("cross-tenant get: got %v, want kb.ErrNotFound", err)
	}
}

func TestList(t *testing.T) {
	r := memory.New()
	userID := uuid.New()
	_, _ = r.Create(context.Background(), userID, "a")
	_, _ = r.Create(context.Background(), userID, "b")
	_, _ = r.Create(context.Background(), uuid.New(), "other")

	kbs, err := r.List(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(kbs) != 2 {
		t.Errorf("count: got %d, want 2", len(kbs))
	}
}

func TestList_Empty(t *testing.T) {
	r := memory.New()
	kbs, err := r.List(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(kbs) != 0 {
		t.Errorf("count: got %d, want 0", len(kbs))
	}
}

func TestRename(t *testing.T) {
	r := memory.New()
	userID := uuid.New()
	created, _ := r.Create(context.Background(), userID, "old")

	updated, err := r.Rename(context.Background(), userID, created.ID, "new")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "new" {
		t.Errorf("name: got %q, want %q", updated.Name, "new")
	}
}

func TestRename_NotFound(t *testing.T) {
	r := memory.New()
	_, err := r.Rename(context.Background(), uuid.New(), uuid.New(), "x")
	if err != kb.ErrNotFound {
		t.Errorf("got %v, want kb.ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	r := memory.New()
	userID := uuid.New()
	created, _ := r.Create(context.Background(), userID, "kb")

	if err := r.Delete(context.Background(), userID, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(context.Background(), userID, created.ID); err != kb.ErrNotFound {
		t.Errorf("after delete: got %v, want kb.ErrNotFound", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	r := memory.New()
	err := r.Delete(context.Background(), uuid.New(), uuid.New())
	if err != kb.ErrNotFound {
		t.Errorf("got %v, want kb.ErrNotFound", err)
	}
}
