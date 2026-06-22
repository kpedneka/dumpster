package account_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/account"
	accountmock "github.com/kunalpednekar/dumpster/internal/account/mock"
	"github.com/kunalpednekar/dumpster/internal/auth"
	authmock "github.com/kunalpednekar/dumpster/internal/auth/mock"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/objectstore/mock"
)

func seedUser(t *testing.T, users *authmock.UserStore, clerkUserID, email string) *auth.User {
	t.Helper()
	u, err := users.GetOrCreateByClerkID(context.Background(), clerkUserID, email)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func seedDoc(t *testing.T, docs *memory.Repository, userID uuid.UUID, key string) {
	t.Helper()
	_, err := docs.Create(context.Background(), &document.Document{
		KBID:        uuid.New(),
		UserID:      userID,
		Filename:    key,
		S3Key:       key,
		ContentType: "text/plain",
		Status:      document.StatusIndexed,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeleter_Delete_happyPath(t *testing.T) {
	identity := accountmock.New()
	objects := mock.New()
	docs := memory.New()
	users := authmock.NewUserStore()

	u := seedUser(t, users, "user_abc123", "alice@example.com")
	seedDoc(t, docs, u.ID, "uploads/a.txt")
	seedDoc(t, docs, u.ID, "uploads/b.txt")
	if err := objects.Put(context.Background(), "uploads/a.txt", strings.NewReader("a"), 1, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(context.Background(), "uploads/b.txt", strings.NewReader("b"), 1, "text/plain"); err != nil {
		t.Fatal(err)
	}

	d := account.New(identity, objects, docs, users)
	if err := d.Delete(context.Background(), u); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if !identity.Deleted("user_abc123") {
		t.Error("expected Clerk identity to be deleted")
	}
	if _, err := objects.Get(context.Background(), "uploads/a.txt"); err == nil {
		t.Error("expected uploads/a.txt to be removed from object store")
	}
	if _, err := objects.Get(context.Background(), "uploads/b.txt"); err == nil {
		t.Error("expected uploads/b.txt to be removed from object store")
	}
	if _, err := users.GetByClerkID(context.Background(), "user_abc123"); !errors.Is(err, auth.ErrUserNotFound) {
		t.Errorf("got %v, want auth.ErrUserNotFound", err)
	}
}

func TestDeleter_Delete_allowsResignup(t *testing.T) {
	identity := accountmock.New()
	objects := mock.New()
	docs := memory.New()
	users := authmock.NewUserStore()

	u := seedUser(t, users, "user_abc123", "alice@example.com")

	d := account.New(identity, objects, docs, users)
	if err := d.Delete(context.Background(), u); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	fresh, err := users.GetOrCreateByClerkID(context.Background(), "user_abc123", "alice@example.com")
	if err != nil {
		t.Fatalf("re-signup should succeed after deletion: %v", err)
	}
	if fresh.ID == u.ID {
		t.Error("re-signup should create a fresh local row, not reuse the deleted one")
	}
}

func TestDeleter_Delete_clerkFailureLeavesLocalRowIntact(t *testing.T) {
	identity := accountmock.New()
	identity.Err = errors.New("clerk: backend unavailable")
	objects := mock.New()
	docs := memory.New()
	users := authmock.NewUserStore()

	u := seedUser(t, users, "user_abc123", "alice@example.com")

	d := account.New(identity, objects, docs, users)
	if err := d.Delete(context.Background(), u); err == nil {
		t.Fatal("expected error when Clerk delete fails")
	}

	if _, err := users.GetByClerkID(context.Background(), "user_abc123"); err != nil {
		t.Errorf("local row should not be deleted when Clerk delete fails: %v", err)
	}
}

func TestDeleter_Delete_partialR2FailureAbortsBeforeClerkAndDB(t *testing.T) {
	identity := accountmock.New()
	objects := mock.New()
	docs := memory.New()
	users := authmock.NewUserStore()

	u := seedUser(t, users, "user_abc123", "alice@example.com")
	seedDoc(t, docs, u.ID, "uploads/a.txt")
	seedDoc(t, docs, u.ID, "uploads/b.txt")
	if err := objects.Put(context.Background(), "uploads/a.txt", strings.NewReader("a"), 1, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(context.Background(), "uploads/b.txt", strings.NewReader("b"), 1, "text/plain"); err != nil {
		t.Fatal(err)
	}
	objects.DeleteErrFor = map[string]error{"uploads/a.txt": errors.New("r2: unavailable")}

	d := account.New(identity, objects, docs, users)
	if err := d.Delete(context.Background(), u); err == nil {
		t.Fatal("expected error when an R2 delete fails")
	}

	if _, err := objects.Get(context.Background(), "uploads/b.txt"); err == nil {
		t.Error("uploads/b.txt should still have been attempted and removed despite uploads/a.txt failing")
	}
	if identity.Deleted("user_abc123") {
		t.Error("Clerk identity should not be deleted when an R2 delete fails")
	}
	if _, err := users.GetByClerkID(context.Background(), "user_abc123"); err != nil {
		t.Errorf("local row should not be deleted when an R2 delete fails: %v", err)
	}
}

func TestDeleter_Delete_zeroDocuments(t *testing.T) {
	identity := accountmock.New()
	objects := mock.New()
	docs := memory.New()
	users := authmock.NewUserStore()

	u := seedUser(t, users, "user_abc123", "alice@example.com")

	d := account.New(identity, objects, docs, users)
	if err := d.Delete(context.Background(), u); err != nil {
		t.Fatalf("Delete with no documents should succeed: %v", err)
	}
	if !identity.Deleted("user_abc123") {
		t.Error("expected Clerk identity to be deleted")
	}
}
