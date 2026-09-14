package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/account"
	"github.com/kunalpednekar/dumpster/internal/session"
	sessionmock "github.com/kunalpednekar/dumpster/internal/session/mock"
)

// accountStatusRequest builds a /account/status request for a session with
// the given createdAt and lastActiveAt, wired against a fresh set of deps.
func accountStatusRequest(t *testing.T, createdAt, lastActiveAt time.Time) *httptest.ResponseRecorder {
	t.Helper()
	deps, _, _, _, _ := defaultDeps()
	sessionID := uuid.New()
	deps.Sessions.(*sessionmock.Store).Seed(&session.Session{
		ID:           sessionID,
		CreatedAt:    createdAt,
		LastActiveAt: lastActiveAt,
	})
	router := NewRouter(deps)

	req := httptest.NewRequest(http.MethodGet, "/api/account/status", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID.String()})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// TestAccountStatus_freshAccount: a brand-new session is well outside both
// expiry windows, so warning_active should be false.
func TestAccountStatus_freshAccount(t *testing.T) {
	now := time.Now()
	w := accountStatusRequest(t, now, now)

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

// TestAccountStatus_nearHardCapExpiry: a session within WarningLeadTime of
// HardCap should report warning_active=true.
func TestAccountStatus_nearHardCapExpiry(t *testing.T) {
	now := time.Now()
	// Created 10 min before HardCap, active recently.
	createdAt := now.Add(-(account.HardCap - 10*time.Minute))
	w := accountStatusRequest(t, createdAt, now)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp accountStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.WarningActive {
		t.Error("session near hard-cap should have warning_active=true")
	}
}

// TestAccountStatus_deletesAt_reflectsNearestClock: the handler reports
// deletes_at as whichever clock fires first. For a recently-active session
// near hard cap, the hard-cap expiry is nearer than idle timeout.
func TestAccountStatus_deletesAt_reflectsNearestClock(t *testing.T) {
	now := time.Now()
	// Created 10 min before hard cap, recently active (middleware will touch it).
	createdAt := now.Add(-(account.HardCap - 10*time.Minute))
	w := accountStatusRequest(t, createdAt, now)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp accountStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	// Hard-cap expiry (~10 min away) is closer than idle timeout (~2 h away).
	hardCapExpiry := createdAt.Add(account.HardCap)
	if resp.DeletesAt.After(hardCapExpiry.Add(time.Second)) {
		t.Errorf("deletes_at %v should be at or before hard-cap expiry %v", resp.DeletesAt, hardCapExpiry)
	}
	if !resp.WarningActive {
		t.Error("session 10 min from hard cap should have warning_active=true")
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
	var resp accountStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.WarningActive {
		t.Error("brand-new session should not be in warning window")
	}
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
