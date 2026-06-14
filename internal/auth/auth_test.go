package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
)

// --- password ---

func TestHashAndCheck(t *testing.T) {
	hash, err := auth.HashPassword("s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.CheckPassword(hash, "s3cr3t"); err != nil {
		t.Fatal("correct password rejected")
	}
	if err := auth.CheckPassword(hash, "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
}

// --- JWT ---

const testSecret = "test-secret-key"

var testUserID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

func TestIssueAndParse(t *testing.T) {
	token, err := auth.IssueToken(testUserID, testSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ParseToken(token, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != testUserID.String() {
		t.Fatalf("userID: got %q, want %q", claims.UserID, testUserID.String())
	}
}

func TestParseToken_expired(t *testing.T) {
	token, err := auth.IssueToken(testUserID, testSecret, -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ParseToken(token, testSecret); err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestParseToken_wrongSecret(t *testing.T) {
	token, err := auth.IssueToken(testUserID, testSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ParseToken(token, "other-secret"); err == nil {
		t.Fatal("expected error for wrong secret")
	}
}

// --- context ---

func TestUserIDContext(t *testing.T) {
	id := uuid.New()
	ctx := auth.WithUserID(context.Background(), id)
	got, ok := auth.UserIDFromContext(ctx)
	if !ok || got != id {
		t.Fatalf("got %v ok=%v, want %v true", got, ok, id)
	}
}

func TestUserIDContext_missing(t *testing.T) {
	_, ok := auth.UserIDFromContext(context.Background())
	if ok {
		t.Fatal("expected ok=false for empty context")
	}
}

// --- middleware ---

func authedHandler(t *testing.T, wantID uuid.UUID) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			t.Error("user UUID missing from context in handler")
		}
		if id != wantID {
			t.Errorf("user UUID: got %v, want %v", id, wantID)
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddleware_valid(t *testing.T) {
	id := uuid.New()
	token, _ := auth.IssueToken(id, testSecret, time.Hour)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	auth.Middleware(testSecret, authedHandler(t, id)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
}

func TestMiddleware_noToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	auth.Middleware(testSecret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	})).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", w.Code)
	}
}

func TestMiddleware_invalidToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	w := httptest.NewRecorder()
	auth.Middleware(testSecret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	})).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", w.Code)
	}
}
