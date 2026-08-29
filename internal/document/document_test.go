package document_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/document/memory"
)

var _ document.Repository = (*memory.Repository)(nil)

func newDoc(userID, kbID uuid.UUID) *document.Document {
	return &document.Document{
		KBID:        kbID,
		UserID:      userID,
		Filename:    "report.pdf",
		S3Key:       "uploads/" + uuid.New().String() + ".pdf",
		ContentType: "application/pdf",
		SizeBytes:   2048,
		Status:      document.StatusPending,
	}
}

func TestDocument_CRUD(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID, kbID := uuid.New(), uuid.New()

	created, err := repo.Create(ctx, newDoc(userID, kbID))
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == uuid.Nil || created.Status != document.StatusPending {
		t.Fatalf("unexpected created: %+v", created)
	}
	if created.SizeBytes != 2048 {
		t.Errorf("size_bytes: got %d, want 2048", created.SizeBytes)
	}

	if err := repo.UpdateStatus(ctx, userID, created.ID, document.StatusIndexed); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get(ctx, userID, created.ID)
	if got.Status != document.StatusIndexed {
		t.Fatalf("status: got %q, want indexed", got.Status)
	}

	list, _ := repo.ListByKB(ctx, userID, kbID)
	if len(list) != 1 {
		t.Fatalf("listByKB: got %d, want 1", len(list))
	}

	if err := repo.Delete(ctx, userID, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, userID, created.ID); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestDocument_TenantIsolation(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	user1, user2, kbID := uuid.New(), uuid.New(), uuid.New()

	doc, _ := repo.Create(ctx, newDoc(user1, kbID))

	if _, err := repo.Get(ctx, user2, doc.ID); err == nil {
		t.Fatal("user2 should not Get user1's document")
	}
	if err := repo.UpdateStatus(ctx, user2, doc.ID, document.StatusFailed); err == nil {
		t.Fatal("user2 should not UpdateStatus on user1's document")
	}
	if err := repo.Delete(ctx, user2, doc.ID); err == nil {
		t.Fatal("user2 should not Delete user1's document")
	}

	list, _ := repo.ListByKB(ctx, user2, kbID)
	if len(list) != 0 {
		t.Fatalf("user2 ListByKB should be empty, got %d", len(list))
	}
}

func TestDocument_ErrNotFound(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID := uuid.New()
	missing := uuid.New()

	for _, tc := range []struct {
		name string
		fn   func() error
	}{
		{"Get", func() error { _, err := repo.Get(ctx, userID, missing); return err }},
		{"UpdateStatus", func() error { return repo.UpdateStatus(ctx, userID, missing, document.StatusFailed) }},
		{"Delete", func() error { return repo.Delete(ctx, userID, missing) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if !errors.Is(err, document.ErrNotFound) {
				t.Errorf("got %v, want errors.Is(err, document.ErrNotFound)", err)
			}
		})
	}
}

func TestDocument_StatusTransitions(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID, kbID := uuid.New(), uuid.New()

	doc, _ := repo.Create(ctx, newDoc(userID, kbID))

	for _, status := range []document.Status{
		document.StatusProcessing,
		document.StatusIndexed,
		document.StatusFailed,
	} {
		if err := repo.UpdateStatus(ctx, userID, doc.ID, status); err != nil {
			t.Fatalf("UpdateStatus to %q: %v", status, err)
		}
		got, _ := repo.Get(ctx, userID, doc.ID)
		if got.Status != status {
			t.Fatalf("status: got %q, want %q", got.Status, status)
		}
	}
}
