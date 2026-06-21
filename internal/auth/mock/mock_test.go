package mock_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/auth/mock"
)

func TestVerifier_Verify(t *testing.T) {
	v := mock.NewVerifier(auth.Identity{ClerkUserID: "user_1", Email: "a@example.com"})
	id, err := v.Verify(context.Background(), "any-token")
	if err != nil {
		t.Fatal(err)
	}
	if id.ClerkUserID != "user_1" || id.Email != "a@example.com" {
		t.Errorf("got %+v", id)
	}
}

func TestErrorVerifier_Verify(t *testing.T) {
	v := mock.NewErrorVerifier(errors.New("bad token"))
	if _, err := v.Verify(context.Background(), "tok"); err == nil {
		t.Fatal("expected error")
	}
}

func TestUserStore_GetOrCreateByClerkID_idempotent(t *testing.T) {
	s := mock.NewUserStore()
	u1, err := s.GetOrCreateByClerkID(context.Background(), "user_1", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := s.GetOrCreateByClerkID(context.Background(), "user_1", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if u1.ID != u2.ID {
		t.Errorf("expected stable ID across calls: got %v then %v", u1.ID, u2.ID)
	}
}

func TestUserStore_GetByClerkID_notFound(t *testing.T) {
	s := mock.NewUserStore()
	if _, err := s.GetByClerkID(context.Background(), "missing"); !errors.Is(err, auth.ErrUserNotFound) {
		t.Errorf("got %v, want ErrUserNotFound", err)
	}
}

func TestUserStore_GetByID(t *testing.T) {
	s := mock.NewUserStore()
	u, err := s.GetOrCreateByClerkID(context.Background(), "user_1", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetByID(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClerkUserID != "user_1" {
		t.Errorf("got %+v", got)
	}
}

func TestUserStore_GetByID_notFound(t *testing.T) {
	s := mock.NewUserStore()
	if _, err := s.GetByID(context.Background(), [16]byte{1}); !errors.Is(err, auth.ErrUserNotFound) {
		t.Errorf("got %v, want ErrUserNotFound", err)
	}
}

func TestUserStore_DeleteByClerkID(t *testing.T) {
	s := mock.NewUserStore()
	u, err := s.GetOrCreateByClerkID(context.Background(), "user_1", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteByClerkID(context.Background(), "user_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetByID(context.Background(), u.ID); !errors.Is(err, auth.ErrUserNotFound) {
		t.Errorf("expected user removed, got %v", err)
	}
}

func TestUserStore_DeleteByClerkID_noop(t *testing.T) {
	s := mock.NewUserStore()
	if err := s.DeleteByClerkID(context.Background(), "missing"); err != nil {
		t.Fatal(err)
	}
}

func TestUserStore_GetOrCreateByClerkID_error(t *testing.T) {
	s := mock.NewUserStore()
	s.GetOrCreateErr = errors.New("db down")
	if _, err := s.GetOrCreateByClerkID(context.Background(), "user_1", "a@example.com"); err == nil {
		t.Fatal("expected error")
	}
}

func TestUserStore_Seed(t *testing.T) {
	s := mock.NewUserStore()
	fixedID := uuid.New()
	s.Seed(&auth.User{ID: fixedID, ClerkUserID: "user_1", Email: "a@example.com"})

	got, err := s.GetOrCreateByClerkID(context.Background(), "user_1", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != fixedID {
		t.Errorf("ID: got %v, want %v", got.ID, fixedID)
	}
}
