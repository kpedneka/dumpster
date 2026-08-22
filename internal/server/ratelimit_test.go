package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	ratelimitMock "github.com/kunalpednekar/dumpster/internal/ratelimit/mock"
)

// TestRateLimiter_blocksExceededIP verifies that the router returns 429 when
// the injected Limiter denies the request, exercising the middleware through
// the Limiter interface with a fake backend (not a real in-memory store).
func TestRateLimiter_blocksExceededIP(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	deps.RateLimiter = ratelimitMock.Deny()
	router := NewRouter(deps)

	// Any path under "/" goes through the rate-limit middleware.
	req := unauthRequest(http.MethodGet, "/account/status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited request: got %d, want 429", w.Code)
	}
}

// TestRateLimiter_allowsUnderLimit verifies that requests pass through to the
// underlying handler when the Limiter permits them.
func TestRateLimiter_allowsUnderLimit(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	deps.RateLimiter = ratelimitMock.New() // allow all
	router := NewRouter(deps)

	// /healthz is on the root mux and bypasses the rate limiter; use a
	// path under "/" to confirm the allow-path reaches the next handler.
	req := unauthRequest(http.MethodGet, "/account/status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Without a session the auth middleware mints one; /account/status then
	// responds 200 or 500 depending on the session store — either way it is
	// not 429, which is what we are asserting.
	if w.Code == http.StatusTooManyRequests {
		t.Fatalf("request should not be rate-limited, got 429")
	}
}

// TestRateLimiter_nilLimiterDisabled verifies that a nil RateLimiter in Deps
// does not install any limiting — all requests proceed normally.
func TestRateLimiter_nilLimiterDisabled(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	// deps.RateLimiter is nil by default
	router := NewRouter(deps)

	req := unauthRequest(http.MethodGet, "/account/status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code == http.StatusTooManyRequests {
		t.Fatalf("nil limiter should not rate-limit requests, got 429")
	}
}
