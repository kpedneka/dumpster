package account_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/account"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	objmock "github.com/kunalpednekar/dumpster/internal/objectstore/mock"
	"github.com/kunalpednekar/dumpster/internal/session"
	sessionmock "github.com/kunalpednekar/dumpster/internal/session/mock"
)

func seedSession(t *testing.T, store *sessionmock.Store) *session.Session {
	t.Helper()
	sess := &session.Session{ID: uuid.New(), CreatedAt: time.Now()}
	store.Seed(sess)
	return sess
}

func seedDoc(t *testing.T, docs *docmem.Repository, userID uuid.UUID, key string) {
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
	sessions := sessionmock.New()
	objects := objmock.New()
	docs := docmem.New()

	s := seedSession(t, sessions)
	seedDoc(t, docs, s.ID, "uploads/a.txt")
	seedDoc(t, docs, s.ID, "uploads/b.txt")
	if err := objects.Put(context.Background(), "uploads/a.txt", strings.NewReader("a"), 1, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(context.Background(), "uploads/b.txt", strings.NewReader("b"), 1, "text/plain"); err != nil {
		t.Fatal(err)
	}

	d := account.New(sessions, objects, docs)
	if err := d.Delete(context.Background(), s); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := objects.Get(context.Background(), "uploads/a.txt"); err == nil {
		t.Error("expected uploads/a.txt to be removed from object store")
	}
	if _, err := objects.Get(context.Background(), "uploads/b.txt"); err == nil {
		t.Error("expected uploads/b.txt to be removed from object store")
	}
	// Session row should be gone.
	if _, err := sessions.GetByID(context.Background(), s.ID); !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("got %v, want session.ErrSessionNotFound", err)
	}
}

func TestDeleter_Delete_allowsResignup(t *testing.T) {
	sessions := sessionmock.New()
	objects := objmock.New()
	docs := docmem.New()

	s := seedSession(t, sessions)

	d := account.New(sessions, objects, docs)
	if err := d.Delete(context.Background(), s); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// A new session can be created with a different ID.
	fresh, err := sessions.Create(context.Background())
	if err != nil {
		t.Fatalf("re-signup (Create) should succeed after deletion: %v", err)
	}
	if fresh.ID == s.ID {
		t.Error("re-signup should create a fresh session ID")
	}
}

func TestDeleter_Delete_objectFailureAborts(t *testing.T) {
	sessions := sessionmock.New()
	objects := objmock.New()
	docs := docmem.New()

	s := seedSession(t, sessions)
	seedDoc(t, docs, s.ID, "uploads/a.txt")
	seedDoc(t, docs, s.ID, "uploads/b.txt")
	if err := objects.Put(context.Background(), "uploads/a.txt", strings.NewReader("a"), 1, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(context.Background(), "uploads/b.txt", strings.NewReader("b"), 1, "text/plain"); err != nil {
		t.Fatal(err)
	}
	objects.DeleteErrFor = map[string]error{"uploads/a.txt": errors.New("object store: unavailable")}

	d := account.New(sessions, objects, docs)
	if err := d.Delete(context.Background(), s); err == nil {
		t.Fatal("expected error when an object delete fails")
	}

	// Session row should still be present — not deleted when objects fail.
	if _, err := sessions.GetByID(context.Background(), s.ID); err != nil {
		t.Errorf("session row should not be deleted when object delete fails: %v", err)
	}
}

func TestDeleter_Delete_zeroDocuments(t *testing.T) {
	sessions := sessionmock.New()
	objects := objmock.New()
	docs := docmem.New()

	s := seedSession(t, sessions)

	d := account.New(sessions, objects, docs)
	if err := d.Delete(context.Background(), s); err != nil {
		t.Fatalf("Delete with no documents should succeed: %v", err)
	}
	if _, err := sessions.GetByID(context.Background(), s.ID); !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("expected session to be deleted, got: %v", err)
	}
}

func TestDeleter_Delete_sessionDeletedLast(t *testing.T) {
	// R2 objects deleted before session row, so a failure in objects leaves
	// session intact for retry.
	sessions := sessionmock.New()
	objects := objmock.New()
	docs := docmem.New()

	s := seedSession(t, sessions)
	seedDoc(t, docs, s.ID, "uploads/x.txt")
	_ = objects.Put(context.Background(), "uploads/x.txt", strings.NewReader("x"), 1, "text/plain")
	objects.DeleteErrFor = map[string]error{"uploads/x.txt": errors.New("r2: timeout")}

	d := account.New(sessions, objects, docs)
	_ = d.Delete(context.Background(), s)

	if _, err := sessions.GetByID(context.Background(), s.ID); err != nil {
		t.Error("session should survive when object delete fails (retry semantics)")
	}
}

func TestDeleter_compileTimeCheck(t *testing.T) {
	var _ account.AccountDeleter = (*account.Deleter)(nil)
}
