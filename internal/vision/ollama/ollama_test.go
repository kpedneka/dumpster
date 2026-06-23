package ollama

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeOllamaServer(t *testing.T, response string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/generate" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"response": response})
	}))
}

func TestDescribe_success(t *testing.T) {
	srv := fakeOllamaServer(t, "A bar chart showing quarterly revenue.")
	defer srv.Close()

	d := newWithClient(srv.URL, "qwen2.5vl", srv.Client())
	desc, err := d.Describe(t.Context(), []byte("fakeimagebytes"))
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if desc != "A bar chart showing quarterly revenue." {
		t.Errorf("desc: got %q", desc)
	}
}

func TestDescribe_unavailableReturnsEmptyNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	d := newWithClient(srv.URL, "qwen2.5vl", srv.Client())
	desc, err := d.Describe(t.Context(), []byte("bytes"))
	if err != nil {
		t.Fatalf("Describe should not return error when VLM is unavailable: %v", err)
	}
	if desc != "" {
		t.Errorf("expected empty description when VLM unavailable, got %q", desc)
	}
}

func TestConfirmScanned_yesResponse(t *testing.T) {
	srv := fakeOllamaServer(t, "yes")
	defer srv.Close()

	d := newWithClient(srv.URL, "qwen2.5vl", srv.Client())
	scanned, err := d.ConfirmScanned(t.Context(), []byte("bytes"))
	if err != nil {
		t.Fatalf("ConfirmScanned: %v", err)
	}
	if !scanned {
		t.Error("expected isScanned=true for 'yes' response")
	}
}

func TestConfirmScanned_noResponse(t *testing.T) {
	srv := fakeOllamaServer(t, "no")
	defer srv.Close()

	d := newWithClient(srv.URL, "qwen2.5vl", srv.Client())
	scanned, err := d.ConfirmScanned(t.Context(), []byte("bytes"))
	if err != nil {
		t.Fatalf("ConfirmScanned: %v", err)
	}
	if scanned {
		t.Error("expected isScanned=false for 'no' response")
	}
}

func TestConfirmScanned_ambiguousDefaultsToScanned(t *testing.T) {
	srv := fakeOllamaServer(t, "I cannot determine this from the image.")
	defer srv.Close()

	d := newWithClient(srv.URL, "qwen2.5vl", srv.Client())
	scanned, err := d.ConfirmScanned(t.Context(), []byte("bytes"))
	if err != nil {
		t.Fatalf("ConfirmScanned: %v", err)
	}
	if !scanned {
		t.Error("expected isScanned=true (skip-safe default) for ambiguous response")
	}
}

func TestConfirmScanned_unavailableDefaultsToScanned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	d := newWithClient(srv.URL, "qwen2.5vl", srv.Client())
	scanned, err := d.ConfirmScanned(t.Context(), []byte("bytes"))
	if err != nil {
		t.Fatalf("ConfirmScanned should not surface error when VLM unavailable: %v", err)
	}
	if !scanned {
		t.Error("expected isScanned=true (skip-safe default) when VLM unavailable")
	}
}

func TestNew(t *testing.T) {
	d := New("http://ollama:11434", "qwen2.5vl")
	if d == nil {
		t.Fatal("New returned nil")
	}
}
