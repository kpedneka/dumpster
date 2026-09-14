package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	NewRouter(Deps{}).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type: got %q, want %q", ct, "application/json")
	}
}

// TestHealthz_NotUnderAPIPrefix guards the one deliberate exception to the
// /api prefix: /healthz must stay reachable at its unprefixed path, since
// the ALB's own health check hits it directly against the container, never
// through CloudFront's /api routing.
func TestHealthz_NotUnderAPIPrefix(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	req := httptest.NewRequest(http.MethodGet, "/api/healthz", nil)
	w := httptest.NewRecorder()

	NewRouter(deps).ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Error("/api/healthz should not resolve -- /healthz is deliberately unprefixed")
	}
}
