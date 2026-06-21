package auth_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/auth/mock"
)

func authedHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			t.Error("user UUID missing from context in handler")
		}
		if id == uuid.Nil {
			t.Error("user UUID is the zero value")
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddleware_valid(t *testing.T) {
	verifier := mock.NewVerifier(auth.Identity{ClerkUserID: "user_123", Email: "alice@example.com"})
	users := mock.NewUserStore()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer some-clerk-session-token")
	w := httptest.NewRecorder()

	auth.Middleware(verifier, users, authedHandler(t)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
}

func TestMiddleware_mapsToSameLocalUserOnRepeatRequests(t *testing.T) {
	verifier := mock.NewVerifier(auth.Identity{ClerkUserID: "user_123", Email: "alice@example.com"})
	users := mock.NewUserStore()

	var firstID, secondID string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.UserIDFromContext(r.Context())
		if firstID == "" {
			firstID = id.String()
		} else {
			secondID = id.String()
		}
		w.WriteHeader(http.StatusOK)
	})

	mw := auth.Middleware(verifier, users, handler)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer tok")
		mw.ServeHTTP(httptest.NewRecorder(), req)
	}

	if firstID == "" || secondID == "" {
		t.Fatal("expected both requests to reach handler")
	}
	if firstID != secondID {
		t.Fatalf("expected stable local user id across requests: got %q then %q", firstID, secondID)
	}
}

func TestMiddleware_noToken(t *testing.T) {
	verifier := mock.NewVerifier(auth.Identity{ClerkUserID: "user_123"})
	users := mock.NewUserStore()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	auth.Middleware(verifier, users, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	})).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", w.Code)
	}
}

func TestMiddleware_invalidToken(t *testing.T) {
	verifier := mock.NewErrorVerifier(errors.New("invalid signature"))
	users := mock.NewUserStore()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	w := httptest.NewRecorder()
	auth.Middleware(verifier, users, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	})).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", w.Code)
	}
}

func TestMiddleware_localStoreError(t *testing.T) {
	verifier := mock.NewVerifier(auth.Identity{ClerkUserID: "user_123"})
	users := mock.NewUserStore()
	users.GetOrCreateErr = errors.New("db unavailable")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	auth.Middleware(verifier, users, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	})).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", w.Code)
	}
}
