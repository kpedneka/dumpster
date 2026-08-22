package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/session"
	sessionmock "github.com/kunalpednekar/dumpster/internal/session/mock"
)

// accountStatusRequest builds a /account/status request for a session with
// the given createdAt timestamp, wired against a fresh set of deps. Returns
// the recorded response.
func accountStatusRequest(t *testing.T, createdAt time.Time) *httptest.ResponseRecorder {
	t.Helper()
	deps, _, _, _, _ := defaultDeps()
	sessionID := uuid.New()
	// Seed the session directly in the store to control CreatedAt.
	deps.Sessions.(*sessionmock.Store).Seed(&session.Session{
		ID:           sessionID,
		CreatedAt:    createdAt,
		LastActiveAt: time.Now(),
	})
	router := NewRouter(deps)

	req := httptest.NewRequest(http.MethodGet, "/account/status", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID.String()})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestAccountStatus_freshAccount(t *testing.T) {
	w := accountStatusRequest(t, time.Now())

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp accountStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.WarningActive {
		t.Error("fresh account should not have warning_active=true")
	}
}

func TestAccountStatus_day6Account(t *testing.T) {
	w := accountStatusRequest(t, time.Now().Add(-6*24*time.Hour))

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp accountStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.WarningActive {
		t.Error("day-6 account should have warning_active=true")
	}
}

// TestAccountStatus_noSession verifies that a request with no cookie mints a
// fresh session (middleware never 401s) and returns a valid account status for
// that new session.
func TestAccountStatus_noSession(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, unauthRequest(http.MethodGet, "/account/status", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("no-session account status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	// A fresh session should have warning_active=false.
	var resp accountStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.WarningActive {
		t.Error("brand-new session should not be in warning window")
	}
	// New session cookie should be set.
	var found bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "session_id" {
			found = true
		}
	}
	if !found {
		t.Error("expected Set-Cookie: session_id for fresh session")
	}
}
