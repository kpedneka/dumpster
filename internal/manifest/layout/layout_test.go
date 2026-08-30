package layout

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const fakePDF = "%PDF-1.4 fake"

func TestExtractRegions_EmptyInput_NoRequestSent(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	ex := New(Config{BaseURL: srv.URL})
	got, err := ex.ExtractRegions(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("ExtractRegions(nil): got (%v, %v), want (nil, nil)", got, err)
	}
	if called {
		t.Error("no HTTP request should be sent for empty input")
	}
}

func TestExtractRegions_PostsToRegionsEndpointAndMapsResponse(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_, _ = w.Write([]byte(`{"regions":[
			{"region_type":"native_text","page_number":1,"bbox":[0,0,1,0.5],"text":"Hello world","image_base64":"","needs_vlm":""},
			{"region_type":"figure","page_number":1,"bbox":[0,0.5,1,1],"text":"","image_base64":"aGVsbG8=","needs_vlm":"describe"}
		]}`))
	}))
	defer srv.Close()

	ex := New(Config{BaseURL: srv.URL})
	got, err := ex.ExtractRegions(context.Background(), []byte(fakePDF))
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/regions" || gotMethod != http.MethodPost {
		t.Errorf("request: got %s %s, want POST /regions", gotMethod, gotPath)
	}
	if gotBody.PDFB64 == "" {
		t.Error("expected non-empty pdf_base64 in request body")
	}

	if len(got) != 2 {
		t.Fatalf("got %d regions, want 2", len(got))
	}

	text := got[0]
	if text.RegionType != "native_text" || text.Text != "Hello world" || text.NeedsVLM != "" {
		t.Errorf("text region: got %+v", text)
	}
	if text.BoundingBox != [4]float64{0, 0, 1, 0.5} {
		t.Errorf("text bbox: got %v", text.BoundingBox)
	}

	fig := got[1]
	if fig.RegionType != "figure" || fig.NeedsVLM != "describe" || fig.ImageBase64 != "aGVsbG8=" {
		t.Errorf("figure region: got %+v", fig)
	}
}

// TestExtractRegions_LogsPeakRSS covers the v4.6 instrumentation: peak RSS
// reported by a completed run should be logged so future machine-sizing
// decisions can be based on a measured distribution, not a few OOM-kill log
// lines from jobs that crashed.
func TestExtractRegions_LogsPeakRSS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"regions":[],"peak_rss_kb":391272}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	ex := New(Config{BaseURL: srv.URL, Logger: log.New(&buf, "", 0)})

	if _, err := ex.ExtractRegions(context.Background(), []byte(fakePDF)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "391272") {
		t.Errorf("expected log output to report peak RSS, got %q", buf.String())
	}
}

func TestExtractRegions_NonOKStatus_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid pdf_base64"}`))
	}))
	defer srv.Close()

	ex := New(Config{BaseURL: srv.URL})
	_, err := ex.ExtractRegions(context.Background(), []byte(fakePDF))
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestExtractRegions_MalformedJSONResponse_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	ex := New(Config{BaseURL: srv.URL})
	_, err := ex.ExtractRegions(context.Background(), []byte(fakePDF))
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestExtractRegions_UnreachableService_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // closed before use: connections to it are refused

	ex := New(Config{BaseURL: srv.URL})
	_, err := ex.ExtractRegions(context.Background(), []byte(fakePDF))
	if err == nil {
		t.Fatal("expected error when the inference service is unreachable")
	}
}

func TestNew_UsesRealHTTPClient(t *testing.T) {
	ex := New(Config{BaseURL: "http://example.invalid"})
	if ex.client == nil {
		t.Fatal("expected New to wire a real HTTP client")
	}
}
