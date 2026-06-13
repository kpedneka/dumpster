package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestIssueAndParse(t *testing.T) {
	token, err := auth.IssueToken(42, testSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ParseToken(token, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != 42 {
		t.Fatalf("userID: got %d, want 42", claims.UserID)
	}
}

func TestParseToken_expired(t *testing.T) {
	token, err := auth.IssueToken(1, testSecret, -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ParseToken(token, testSecret); err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestParseToken_wrongSecret(t *testing.T) {
	token, err := auth.IssueToken(1, testSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.ParseToken(token, "other-secret"); err == nil {
		t.Fatal("expected error for wrong secret")
	}
}

// --- context ---

func TestUserIDContext(t *testing.T) {
	ctx := auth.WithUserID(context.Background(), 7)
	id, ok := auth.UserIDFromContext(ctx)
	if !ok || id != 7 {
		t.Fatalf("got id=%d ok=%v, want 7 true", id, ok)
	}
}

func TestUserIDContext_missing(t *testing.T) {
	_, ok := auth.UserIDFromContext(context.Background())
	if ok {
		t.Fatal("expected ok=false for empty context")
	}
}

// --- middleware ---

func authedHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			t.Error("UserID missing from context in handler")
		}
		if id != 99 {
			t.Errorf("UserID: got %d, want 99", id)
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddleware_valid(t *testing.T) {
	token, _ := auth.IssueToken(99, testSecret, time.Hour)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	auth.Middleware(testSecret, authedHandler(t)).ServeHTTP(w, req)

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
