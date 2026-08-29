package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestSpaHandler_htmlRequest_servesIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>SPA</html>"), 0644); err != nil {
		t.Fatal(err)
	}

	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})

	h := spaHandler(dir, stub)

	// A client-side route (e.g. /kbs/123) has no matching file on disk, so
	// it falls back to index.html for React Router to handle.
	req := httptest.NewRequest(http.MethodGet, "/kbs", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.9")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<html>") {
		t.Errorf("body: expected HTML content, got %q", w.Body.String())
	}
}

func TestSpaHandler_jsonRequest_passesThrough(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})

	h := spaHandler(t.TempDir(), stub)

	req := httptest.NewRequest(http.MethodGet, "/kbs", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
}

func TestSpaHandler_postRequest_passesThrough(t *testing.T) {
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	// Even with text/html in Accept, POST must not serve the SPA.
	h := spaHandler(t.TempDir(), stub)

	req := httptest.NewRequest(http.MethodPost, "/kbs", nil)
	req.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", w.Code)
	}
}

// TestSpaHandler_rootStaticFile_served covers a real bug: favicons and other
// files copied verbatim from web/public/ (e.g. favicon.svg) live at the SPA
// dir's root, not under /assets/. A request for one has an Accept header
// like "image/*,*/*;q=0.8" — no "text/html" — so it used to fall straight
// through past the old /assets/-only static route into the API mux and
// 404, regardless of what the browser's <link rel="icon"> pointed at.
func TestSpaHandler_rootStaticFile_served(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "favicon.svg"), []byte("<svg>icon</svg>"), 0644); err != nil {
		t.Fatal(err)
	}

	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	h := spaHandler(dir, stub)

	req := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 — body: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "<svg>icon</svg>") {
		t.Errorf("body: got %q, want the svg file's content", w.Body.String())
	}
}

// TestSpaHandler_pathTraversal_blocked guards the existing-file check against
// escaping SPADir via "..".
func TestSpaHandler_pathTraversal_blocked(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>SPA</html>"), 0644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("do not serve me"), 0644); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(secret) }()

	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h := spaHandler(dir, stub)

	req := httptest.NewRequest(http.MethodGet, "/../secret.txt", nil)
	req.Header.Set("Accept", "*/*")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if strings.Contains(w.Body.String(), "do not serve me") {
		t.Fatal("path traversal served a file outside SPADir")
	}
}

func TestNewRouter_staticAsset_served(t *testing.T) {
	dir := t.TempDir()
	assetsDir := filepath.Join(dir, "assets")
	if err := os.Mkdir(assetsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetsDir, "main.js"), []byte("// js"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/main.js", nil)
	w := httptest.NewRecorder()

	NewRouter(Deps{SPADir: dir}).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusOK)
	}
}

// TestNewRouter_rootStaticAsset_served is the end-to-end version of
// TestSpaHandler_rootStaticFile_served, through the real router.
func TestNewRouter_rootStaticAsset_served(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "favicon.svg"), []byte("<svg/>"), 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	req.Header.Set("Accept", "image/*,*/*;q=0.8")
	w := httptest.NewRecorder()

	NewRouter(Deps{SPADir: dir}).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d — body: %s", w.Code, http.StatusOK, w.Body)
	}
}
