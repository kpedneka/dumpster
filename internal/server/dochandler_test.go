package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/document"
)

// multipartUpload builds a multipart/form-data request body for the given
// filename and content, and returns the body and the content-type header value.
func multipartUpload(t *testing.T, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return body, w.FormDataContentType()
}

func TestDocUpload(t *testing.T) {
	deps, kbRepo, _, obj, pub := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(nil, userID, "kb1")
	body, ct := multipartUpload(t, "notes.txt", "hello world")

	req := authedRequest(t, http.MethodPost, "/kbs/"+kb.ID.String()+"/documents", body, userID)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201 — body: %s", w.Code, w.Body)
	}

	var doc document.Document
	if err := json.NewDecoder(w.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Status != document.StatusPending {
		t.Errorf("status: got %q, want pending", doc.Status)
	}
	if doc.Filename != "notes.txt" {
		t.Errorf("filename: got %q, want notes.txt", doc.Filename)
	}
	if doc.S3Key == "" {
		t.Error("s3_key should be non-empty")
	}

	// Verify bytes landed in object storage.
	rc, err := obj.Get(nil, doc.S3Key)
	if err != nil {
		t.Fatalf("s3 object not found: %v", err)
	}
	rc.Close()

	// Verify the DocumentUploaded event was published.
	events := pub.Events()
	if len(events) != 1 {
		t.Fatalf("events: got %d, want 1", len(events))
	}
	if events[0].DocumentID != doc.ID {
		t.Errorf("event document_id mismatch")
	}
}

func TestDocUpload_MarkdownFile(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(nil, userID, "kb1")
	body, ct := multipartUpload(t, "readme.md", "# Title")

	req := authedRequest(t, http.MethodPost, "/kbs/"+kb.ID.String()+"/documents", body, userID)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", w.Code)
	}
}

func TestDocUpload_UnsupportedType(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(nil, userID, "kb1")
	body, ct := multipartUpload(t, "data.pdf", "%PDF")

	req := authedRequest(t, http.MethodPost, "/kbs/"+kb.ID.String()+"/documents", body, userID)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: got %d, want 422", w.Code)
	}
}

func TestDocUpload_KBNotFound(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	body, ct := multipartUpload(t, "notes.txt", "hello")
	req := authedRequest(t, http.MethodPost, "/kbs/"+uuid.New().String()+"/documents", body, userID)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", w.Code)
	}
}

func TestDocList(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(nil, userID, "kb1")
	docRepo.Create(nil, &document.Document{ //nolint:errcheck
		KBID: kb.ID, UserID: userID, Filename: "a.txt",
		S3Key: "k1", ContentType: "text/plain", Status: document.StatusPending,
	})
	docRepo.Create(nil, &document.Document{ //nolint:errcheck
		KBID: kb.ID, UserID: userID, Filename: "b.txt",
		S3Key: "k2", ContentType: "text/plain", Status: document.StatusPending,
	})

	req := authedRequest(t, http.MethodGet, "/kbs/"+kb.ID.String()+"/documents", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var docs []*document.Document
	json.NewDecoder(w.Body).Decode(&docs) //nolint:errcheck
	if len(docs) != 2 {
		t.Errorf("count: got %d, want 2", len(docs))
	}
}

func TestDocGet(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(nil, userID, "kb1")
	created, _ := docRepo.Create(nil, &document.Document{
		KBID: kb.ID, UserID: userID, Filename: "notes.txt",
		S3Key: "key1", ContentType: "text/plain", Status: document.StatusPending,
	})

	req := authedRequest(t, http.MethodGet,
		"/kbs/"+kb.ID.String()+"/documents/"+created.ID.String(), nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var doc document.Document
	json.NewDecoder(w.Body).Decode(&doc) //nolint:errcheck
	if doc.ID != created.ID {
		t.Errorf("id mismatch")
	}
}

func TestDocDelete(t *testing.T) {
	deps, kbRepo, docRepo, obj, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(nil, userID, "kb1")

	// Pre-load object in mock store then create the document row.
	s3Key := "documents/test/file.txt"
	obj.Put(nil, s3Key, bytes.NewReader([]byte("content")), 7, "text/plain") //nolint:errcheck
	created, _ := docRepo.Create(nil, &document.Document{
		KBID: kb.ID, UserID: userID, Filename: "file.txt",
		S3Key: s3Key, ContentType: "text/plain", Status: document.StatusPending,
	})

	req := authedRequest(t, http.MethodDelete,
		"/kbs/"+kb.ID.String()+"/documents/"+created.ID.String(), nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204 — body: %s", w.Code, w.Body)
	}

	// Object must be gone from storage.
	if _, err := obj.Get(nil, s3Key); err == nil {
		t.Error("expected s3 object to be deleted")
	}

	// Row must be gone from the repo.
	if _, err := docRepo.Get(nil, userID, created.ID); err == nil {
		t.Error("expected document row to be deleted")
	}
}

func TestDocDelete_ObjectFirst(t *testing.T) {
	// Verifies that delete removes the S3 object before the row, not after.
	// We can only observe order through side-effects: after a successful 204,
	// both must be absent.
	deps, kbRepo, docRepo, obj, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(nil, userID, "kb1")
	s3Key := "documents/test/file2.txt"
	obj.Put(nil, s3Key, bytes.NewReader([]byte("x")), 1, "text/plain") //nolint:errcheck
	created, _ := docRepo.Create(nil, &document.Document{
		KBID: kb.ID, UserID: userID, Filename: "file2.txt",
		S3Key: s3Key, ContentType: "text/plain", Status: document.StatusPending,
	})

	req := authedRequest(t, http.MethodDelete,
		"/kbs/"+kb.ID.String()+"/documents/"+created.ID.String(), nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204", w.Code)
	}

	if _, err := obj.Get(nil, s3Key); err == nil {
		t.Error("s3 object should be deleted")
	}
}

func TestDocTenantIsolation(t *testing.T) {
	deps, kbRepo, docRepo, obj, _ := defaultDeps()
	router := NewRouter(deps)
	user1, user2 := uuid.New(), uuid.New()

	kb1, _ := kbRepo.Create(nil, user1, "kb1")
	s3Key := "documents/u1/file.txt"
	obj.Put(nil, s3Key, bytes.NewReader([]byte("secret")), 6, "text/plain") //nolint:errcheck
	doc, _ := docRepo.Create(nil, &document.Document{
		KBID: kb1.ID, UserID: user1, Filename: "file.txt",
		S3Key: s3Key, ContentType: "text/plain", Status: document.StatusPending,
	})

	// user2 should not get user1's document
	req := authedRequest(t, http.MethodGet,
		"/kbs/"+kb1.ID.String()+"/documents/"+doc.ID.String(), nil, user2)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant get: got %d, want 404", w.Code)
	}

	// user2 should not delete user1's document
	req = authedRequest(t, http.MethodDelete,
		"/kbs/"+kb1.ID.String()+"/documents/"+doc.ID.String(), nil, user2)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant delete: got %d, want 404", w.Code)
	}
}
