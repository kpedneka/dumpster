package inference

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbed_EmptyInput_NoRequestSent(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	e := NewDocumentEmbedder(srv.URL)
	got, err := e.Embed(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("Embed(nil): got (%v, %v), want (nil, nil)", got, err)
	}
	if called {
		t.Error("no HTTP request should be sent for empty input")
	}
}

func TestEmbed_DocumentEmbedder_SendsIsQueryFalse(t *testing.T) {
	var gotBody embedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" || r.Method != http.MethodPost {
			t.Errorf("request: got %s %s, want POST /embeddings", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{{0.1, 0.2}}, Dims: 2})
	}))
	defer srv.Close()

	e := NewDocumentEmbedder(srv.URL)
	got, err := e.Embed(context.Background(), []string{"a document chunk"})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody.IsQuery {
		t.Error("document embedder must send is_query: false")
	}
	if len(got) != 1 || got[0][0] != 0.1 || got[0][1] != 0.2 {
		t.Errorf("unexpected embeddings: %+v", got)
	}
}

func TestEmbed_QueryEmbedder_SendsIsQueryTrue(t *testing.T) {
	var gotBody embedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{{0.3, 0.4}}, Dims: 2})
	}))
	defer srv.Close()

	e := NewQueryEmbedder(srv.URL)
	_, err := e.Embed(context.Background(), []string{"what is the capital of France?"})
	if err != nil {
		t.Fatal(err)
	}
	if !gotBody.IsQuery {
		t.Error("query embedder must send is_query: true")
	}
}

func TestEmbed_MismatchedResponseCount_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{{0.1}}, Dims: 1})
	}))
	defer srv.Close()

	e := NewDocumentEmbedder(srv.URL)
	_, err := e.Embed(context.Background(), []string{"one", "two"})
	if err == nil {
		t.Fatal("expected error when response embedding count does not match input count")
	}
}

func TestEmbed_NonOKStatus_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"malformed request"}`))
	}))
	defer srv.Close()

	e := NewDocumentEmbedder(srv.URL)
	_, err := e.Embed(context.Background(), []string{"hi"})
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestEmbed_MalformedJSONResponse_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	e := NewDocumentEmbedder(srv.URL)
	_, err := e.Embed(context.Background(), []string{"hi"})
	if err == nil {
		t.Fatal("expected error for malformed JSON response")
	}
}

func TestEmbed_UnreachableService_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // closed before use: connections to it are refused

	e := NewDocumentEmbedder(srv.URL)
	_, err := e.Embed(context.Background(), []string{"hi"})
	if err == nil {
		t.Fatal("expected error when the inference service is unreachable")
	}
}

func TestEmbed_ContextCancellation_Propagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	e := NewDocumentEmbedder(srv.URL)
	_, err := e.Embed(ctx, []string{"hi"})
	if err == nil {
		t.Fatal("expected error when context is already cancelled")
	}
}

func TestDims_ReturnsBGESmallDimensionality(t *testing.T) {
	e := NewDocumentEmbedder("http://example.invalid")
	if got := e.Dims(); got != 384 {
		t.Errorf("Dims() = %d, want 384", got)
	}
}
