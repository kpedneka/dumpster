package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	authmock "github.com/kunalpednekar/dumpster/internal/auth/mock"
)

// accountStatusRequest builds an authed /account/status request against a
// freshly-seeded plain authmock.UserStore (bypassing clerkIDForUserStore,
// whose GetOrCreateByClerkID unconditionally re-seeds a blank CreatedAt on
// every request — fine for handlers that don't care about CreatedAt, but
// not for this one).
func accountStatusRequest(t *testing.T, createdAt time.Time) *httptest.ResponseRecorder {
	t.Helper()
	deps, _, _, _, _ := defaultDeps()
	users := authmock.NewUserStore()
	users.Seed(&auth.User{ID: uuid.New(), ClerkUserID: "user_test", Email: "a@example.com", CreatedAt: createdAt})
	deps.Users = users
	router := NewRouter(deps)

	req := httptest.NewRequest(http.MethodGet, "/account/status", nil)
	req.Header.Set("Authorization", "Bearer user_test")
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

func TestAccountStatus_requiresAuth(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/account/status", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", w.Code)
	}
}
