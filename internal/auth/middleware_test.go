package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	sessionmock "github.com/kunalpednekar/dumpster/internal/session/mock"
	statsmem "github.com/kunalpednekar/dumpster/internal/stats/memory"
)

const sessionCookie = "session_id"

func sessionHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			t.Error("session UUID missing from context in handler")
		}
		if id == uuid.Nil {
			t.Error("session UUID is the zero value")
		}
		w.WriteHeader(http.StatusOK)
	})
}

// TestMiddleware_noCookie_mintsSession verifies that a request with no
// session cookie receives a freshly minted session: a Set-Cookie header is
// set and the handler sees a non-nil UUID in context.
func TestMiddleware_noCookie_mintsSession(t *testing.T) {
	sessions := sessionmock.New()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	auth.Middleware(sessions, true, nil, sessionHandler(t)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var found bool
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			found = true
			if c.HttpOnly == false {
				t.Error("session cookie must be HttpOnly")
			}
			if _, err := uuid.Parse(c.Value); err != nil {
				t.Errorf("session cookie value is not a valid UUID: %q", c.Value)
			}
		}
	}
	if !found {
		t.Error("expected Set-Cookie: session_id header for new session")
	}
}

// TestMiddleware_secureFlag_followsParameter verifies that the Set-Cookie
// Secure attribute tracks the secure parameter exactly. This is the guard
// against the local-dev bug where a hardcoded Secure:true caused browsers to
// silently drop the cookie over plain http, minting a new session (and a new
// tenant identity) on every request.
func TestMiddleware_secureFlag_followsParameter(t *testing.T) {
	for _, secure := range []bool{true, false} {
		sessions := sessionmock.New()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()

		auth.Middleware(sessions, secure, nil, sessionHandler(t)).ServeHTTP(w, req)

		var found bool
		for _, c := range w.Result().Cookies() {
			if c.Name == sessionCookie {
				found = true
				if c.Secure != secure {
					t.Errorf("secure=%v: cookie Secure attribute = %v, want %v", secure, c.Secure, secure)
				}
			}
		}
		if !found {
			t.Fatalf("secure=%v: expected Set-Cookie: session_id header", secure)
		}
	}
}

// TestMiddleware_validCookie_resumesSession verifies that a request carrying
// a cookie for an existing session resumes that session (same UUID in
// context) and calls Touch on the store.
func TestMiddleware_validCookie_resumesSession(t *testing.T) {
	sessions := sessionmock.New()
	// Pre-create a session so GetByID succeeds.
	sess, err := sessions.Create(context.TODO())
	if err != nil {
		t.Fatal(err)
	}

	var gotID uuid.UUID
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, _ = auth.UserIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess.ID.String()})
	w := httptest.NewRecorder()

	auth.Middleware(sessions, true, nil, handler).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if gotID != sess.ID {
		t.Errorf("context userID: got %v, want %v", gotID, sess.ID)
	}
	// Touch must have been called for the session.
	if len(sessions.Touched) == 0 || sessions.Touched[0] != sess.ID {
		t.Errorf("expected Touch(%v), got %v", sess.ID, sessions.Touched)
	}
}

// TestMiddleware_sweptSession_mintsFresh verifies that a request whose
// cookie points to a session that no longer exists (already swept) gets a
// fresh session minted rather than an error. The swept cookie does not
// cause the middleware to 401 or 500.
func TestMiddleware_sweptSession_mintsFresh(t *testing.T) {
	sessions := sessionmock.New()
	// Create then delete a session to simulate a swept session.
	swept, _ := sessions.Create(context.TODO())
	_ = sessions.Delete(context.TODO(), swept.ID)

	var gotID uuid.UUID
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID, _ = auth.UserIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: swept.ID.String()})
	w := httptest.NewRecorder()

	auth.Middleware(sessions, true, nil, handler).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	// Handler should receive a new session, not the swept one.
	if gotID == swept.ID {
		t.Error("expected a fresh session ID, not the swept one")
	}
	if gotID == uuid.Nil {
		t.Error("expected a non-nil session ID for fresh session")
	}
	// A new Set-Cookie header should be present.
	var found bool
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			found = true
		}
	}
	if !found {
		t.Error("expected Set-Cookie for fresh session after swept cookie")
	}
}

// TestMiddleware_sameSessionAcrossRequests verifies that two requests
// carrying the same valid cookie both resolve to the same session UUID,
// matching the Clerk-era guarantee that session identity is stable.
func TestMiddleware_sameSessionAcrossRequests(t *testing.T) {
	sessions := sessionmock.New()
	sess, _ := sessions.Create(context.TODO())

	var ids []uuid.UUID
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.UserIDFromContext(r.Context())
		ids = append(ids, id)
		w.WriteHeader(http.StatusOK)
	})

	mw := auth.Middleware(sessions, true, nil, handler)
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess.ID.String()})
		mw.ServeHTTP(httptest.NewRecorder(), req)
	}

	if len(ids) != 2 {
		t.Fatal("expected both requests to reach handler")
	}
	if ids[0] != ids[1] {
		t.Errorf("session ID must be stable: first=%v second=%v", ids[0], ids[1])
	}
}

// TestMiddleware_recordsSessionCreated_onlyOnMint verifies statsRepo gets a
// RecordSessionCreated call exactly once per freshly minted session — not
// once per request, since a request resuming an existing session (via a
// valid cookie) shouldn't inflate the counter.
func TestMiddleware_recordsSessionCreated_onlyOnMint(t *testing.T) {
	sessions := sessionmock.New()
	statsRepo := statsmem.New()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	auth.Middleware(sessions, true, statsRepo, sessionHandler(t)).ServeHTTP(w, req)

	snap, err := statsRepo.Get(context.TODO())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if snap.SessionsCreated != 1 {
		t.Fatalf("SessionsCreated after mint: got %d, want 1", snap.SessionsCreated)
	}

	// Resume the just-minted session on a second request; the counter must
	// not increment again.
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("expected a session cookie from the first request")
	}
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(cookie)
	auth.Middleware(sessions, true, statsRepo, sessionHandler(t)).ServeHTTP(httptest.NewRecorder(), req2)

	snap, err = statsRepo.Get(context.TODO())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if snap.SessionsCreated != 1 {
		t.Errorf("SessionsCreated after resuming an existing session: got %d, want still 1", snap.SessionsCreated)
	}
}
