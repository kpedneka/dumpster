package document_test

import (
	"context"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/document/memory"
)

var _ document.Repository = (*memory.Repository)(nil)

func newDoc(userID, kbID int64) *document.Document {
	return &document.Document{
		KBID:        kbID,
		UserID:      userID,
		Filename:    "report.pdf",
		S3Key:       "uploads/report.pdf",
		ContentType: "application/pdf",
		Status:      document.StatusPending,
	}
}

func TestDocument_CRUD(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()

	created, err := repo.Create(ctx, newDoc(1, 10))
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.Status != document.StatusPending {
		t.Fatalf("unexpected created: %+v", created)
	}

	if err := repo.UpdateStatus(ctx, 1, created.ID, document.StatusIndexed); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get(ctx, 1, created.ID)
	if got.Status != document.StatusIndexed {
		t.Fatalf("status: got %q, want indexed", got.Status)
	}

	list, _ := repo.ListByKB(ctx, 1, 10)
	if len(list) != 1 {
		t.Fatalf("listByKB: got %d, want 1", len(list))
	}

	if err := repo.Delete(ctx, 1, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, 1, created.ID); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestDocument_TenantIsolation(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()

	doc, _ := repo.Create(ctx, newDoc(1, 10))

	if _, err := repo.Get(ctx, 2, doc.ID); err == nil {
		t.Fatal("user2 should not Get user1's document")
	}
	if err := repo.UpdateStatus(ctx, 2, doc.ID, document.StatusFailed); err == nil {
		t.Fatal("user2 should not UpdateStatus on user1's document")
	}
	if err := repo.Delete(ctx, 2, doc.ID); err == nil {
		t.Fatal("user2 should not Delete user1's document")
	}

	list, _ := repo.ListByKB(ctx, 2, 10)
	if len(list) != 0 {
		t.Fatalf("user2 ListByKB should be empty, got %d", len(list))
	}
}

func TestDocument_StatusTransitions(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()

	doc, _ := repo.Create(ctx, newDoc(1, 10))

	for _, status := range []document.Status{
		document.StatusProcessing,
		document.StatusIndexed,
		document.StatusFailed,
	} {
		if err := repo.UpdateStatus(ctx, 1, doc.ID, status); err != nil {
			t.Fatalf("UpdateStatus to %q: %v", status, err)
		}
		got, _ := repo.Get(ctx, 1, doc.ID)
		if got.Status != status {
			t.Fatalf("status: got %q, want %q", got.Status, status)
		}
	}
}
