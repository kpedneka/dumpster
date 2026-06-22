package memory_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/document/memory"
)

func seed(t *testing.T, r *memory.Repository, userID, kbID uuid.UUID, filename string) *document.Document {
	t.Helper()
	d, err := r.Create(context.Background(), &document.Document{
		KBID:        kbID,
		UserID:      userID,
		Filename:    filename,
		S3Key:       "s3/" + filename,
		ContentType: "text/plain",
		Status:      document.StatusPending,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCreate(t *testing.T) {
	r := memory.New()
	userID, kbID := uuid.New(), uuid.New()

	d := seed(t, r, userID, kbID, "notes.txt")
	if d.ID == uuid.Nil {
		t.Error("expected non-nil ID")
	}
	if d.Status != document.StatusPending {
		t.Errorf("status: got %q, want pending", d.Status)
	}
}

func TestGet(t *testing.T) {
	r := memory.New()
	userID, kbID := uuid.New(), uuid.New()
	created := seed(t, r, userID, kbID, "a.txt")

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
	_, err := r.Get(context.Background(), uuid.New(), uuid.New())
	if err != document.ErrNotFound {
		t.Errorf("got %v, want document.ErrNotFound", err)
	}
}

func TestGet_WrongTenant(t *testing.T) {
	r := memory.New()
	owner := uuid.New()
	created := seed(t, r, owner, uuid.New(), "f.txt")

	_, err := r.Get(context.Background(), uuid.New(), created.ID)
	if err != document.ErrNotFound {
		t.Errorf("cross-tenant get: got %v, want document.ErrNotFound", err)
	}
}

func TestListByKB(t *testing.T) {
	r := memory.New()
	userID, kbID := uuid.New(), uuid.New()
	seed(t, r, userID, kbID, "a.txt")
	seed(t, r, userID, kbID, "b.txt")
	seed(t, r, userID, uuid.New(), "other.txt") // different KB

	docs, err := r.ListByKB(context.Background(), userID, kbID)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Errorf("count: got %d, want 2", len(docs))
	}
}

func TestListByKB_Empty(t *testing.T) {
	r := memory.New()
	docs, err := r.ListByKB(context.Background(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 {
		t.Errorf("count: got %d, want 0", len(docs))
	}
}

func TestListByUserID(t *testing.T) {
	r := memory.New()
	userID := uuid.New()
	seed(t, r, userID, uuid.New(), "a.txt")
	seed(t, r, userID, uuid.New(), "b.txt") // different KB, same user
	seed(t, r, uuid.New(), uuid.New(), "other.txt") // different user

	docs, err := r.ListByUserID(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Errorf("count: got %d, want 2", len(docs))
	}
}

func TestListByUserID_Empty(t *testing.T) {
	r := memory.New()
	docs, err := r.ListByUserID(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 {
		t.Errorf("count: got %d, want 0", len(docs))
	}
}

func TestUpdateStatus(t *testing.T) {
	r := memory.New()
	userID, kbID := uuid.New(), uuid.New()
	created := seed(t, r, userID, kbID, "doc.txt")

	if err := r.UpdateStatus(context.Background(), userID, created.ID, document.StatusIndexed); err != nil {
		t.Fatal(err)
	}

	got, _ := r.Get(context.Background(), userID, created.ID)
	if got.Status != document.StatusIndexed {
		t.Errorf("status: got %q, want indexed", got.Status)
	}
}

func TestUpdateStatus_NotFound(t *testing.T) {
	r := memory.New()
	err := r.UpdateStatus(context.Background(), uuid.New(), uuid.New(), document.StatusFailed)
	if err != document.ErrNotFound {
		t.Errorf("got %v, want document.ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	r := memory.New()
	userID, kbID := uuid.New(), uuid.New()
	created := seed(t, r, userID, kbID, "del.txt")

	if err := r.Delete(context.Background(), userID, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(context.Background(), userID, created.ID); err != document.ErrNotFound {
		t.Errorf("after delete: got %v, want document.ErrNotFound", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	r := memory.New()
	err := r.Delete(context.Background(), uuid.New(), uuid.New())
	if err != document.ErrNotFound {
		t.Errorf("got %v, want document.ErrNotFound", err)
	}
}
