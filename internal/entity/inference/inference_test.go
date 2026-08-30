package inference

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/entity"
)

func TestExtract_EmptyInputs_NoRequestSent(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	ex := New(srv.URL)
	got, err := ex.Extract(context.Background(), nil, []entity.Type{"person"})
	if err != nil || got != nil {
		t.Fatalf("Extract(no chunks): got (%v, %v), want (nil, nil)", got, err)
	}
	got, err = ex.Extract(context.Background(), []*chunk.Chunk{{Text: "hi"}}, nil)
	if err != nil || got != nil {
		t.Fatalf("Extract(no types): got (%v, %v), want (nil, nil)", got, err)
	}
	if called {
		t.Error("no HTTP request should be sent when there is nothing to extract")
	}
}

func TestExtract_PostsToEntitiesEndpointAndMapsResponse(t *testing.T) {
	docID, kbID, userID, chunkID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	c := &chunk.Chunk{ID: chunkID, DocumentID: docID, KBID: kbID, UserID: userID, Text: "Ada Lovelace wrote notes."}

	var gotPath, gotMethod string
	var gotBody entitiesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(entitiesResponse{
			Entities: []entityResponse{
				{ChunkID: chunkID.String(), Type: "person", Text: "Ada Lovelace", Start: 0, End: 12, Score: 0.93},
			},
		})
	}))
	defer srv.Close()

	ex := New(srv.URL)
	got, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"})
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/entities" || gotMethod != http.MethodPost {
		t.Errorf("request: got %s %s, want POST /entities", gotMethod, gotPath)
	}
	if len(gotBody.Chunks) != 1 || gotBody.Chunks[0].ChunkID != chunkID.String() {
		t.Errorf("request body chunks: got %+v", gotBody.Chunks)
	}

	if len(got) != 1 {
		t.Fatalf("got %d entities, want 1", len(got))
	}
	e := got[0]
	if e.DocumentID != docID || e.KBID != kbID || e.UserID != userID || e.ChunkID != chunkID {
		t.Errorf("entity not populated from source chunk: %+v", e)
	}
	if e.Type != "person" || e.Text != "Ada Lovelace" || e.Start != 0 || e.End != 12 {
		t.Errorf("unexpected entity fields: %+v", e)
	}
	if e.Score != 0.93 {
		t.Errorf("score: got %v, want 0.93", e.Score)
	}
}

func TestExtract_UnknownChunkID_Skipped(t *testing.T) {
	c := &chunk.Chunk{ID: uuid.New(), Text: "hello"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(entitiesResponse{
			Entities: []entityResponse{{ChunkID: uuid.New().String(), Type: "person", Text: "X", Start: 0, End: 1, Score: 0.5}},
		})
	}))
	defer srv.Close()

	ex := New(srv.URL)
	got, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected entities for unknown chunk ids to be skipped, got %d", len(got))
	}
}

func TestExtract_NonOKStatus_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"malformed request"}`))
	}))
	defer srv.Close()

	ex := New(srv.URL)
	_, err := ex.Extract(context.Background(), []*chunk.Chunk{{ID: uuid.New(), Text: "hi"}}, []entity.Type{"person"})
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestExtract_MalformedJSONResponse_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	ex := New(srv.URL)
	_, err := ex.Extract(context.Background(), []*chunk.Chunk{{ID: uuid.New(), Text: "hi"}}, []entity.Type{"person"})
	if err == nil {
		t.Fatal("expected error for malformed JSON response")
	}
}

func TestExtract_UnreachableService_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // closed before use: connections to it are refused

	ex := New(srv.URL)
	_, err := ex.Extract(context.Background(), []*chunk.Chunk{{ID: uuid.New(), Text: "hi"}}, []entity.Type{"person"})
	if err == nil {
		t.Fatal("expected error when the inference service is unreachable")
	}
}

func TestExtract_ContextCancellation_Propagates(t *testing.T) {
	// Unlike the retired subprocess protocol's blocking stdin/stdout read,
	// an HTTP request honors context cancellation — this is the reliability
	// win named on the System Architecture page's Inference Service
	// sub-page, verified here directly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ex := New(srv.URL)
	_, err := ex.Extract(ctx, []*chunk.Chunk{{ID: uuid.New(), Text: "hi"}}, []entity.Type{"person"})
	if err == nil {
		t.Fatal("expected error when context is already cancelled")
	}
}

func TestNew_UsesRealHTTPClient(t *testing.T) {
	ex := New("http://example.invalid")
	if ex.client == nil {
		t.Fatal("expected New to wire a real HTTP client")
	}
}

var _ entity.Extractor = (*Extractor)(nil)
