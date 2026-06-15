package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/queue"
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
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body, w.FormDataContentType()
}

// failPublisher always returns an error from PublishDocumentUploaded.
type failPublisher struct{}

func (p *failPublisher) PublishDocumentUploaded(_ context.Context, _ queue.DocumentUploaded) error {
	return errors.New("queue unavailable")
}

func TestDocUpload(t *testing.T) {
	deps, kbRepo, _, obj, pub := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(context.TODO(), userID, "kb1")
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
	rc, err := obj.Get(context.TODO(), doc.S3Key)
	if err != nil {
		t.Fatalf("s3 object not found: %v", err)
	}
	_ = rc.Close()

	// Verify the DocumentUploaded event was published.
	events := pub.Events()
	if len(events) != 1 {
		t.Fatalf("events: got %d, want 1", len(events))
	}
	if events[0].DocumentID != doc.ID {
		t.Errorf("event document_id mismatch")
	}
	if events[0].UserID != userID {
		t.Errorf("event user_id mismatch")
	}
}

func TestDocUpload_MarkdownFile(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(context.TODO(), userID, "kb1")
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

	kb, _ := kbRepo.Create(context.TODO(), userID, "kb1")
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

// --- Finding 1: path traversal ---

func TestDocUpload_PathTraversal(t *testing.T) {
	deps, kbRepo, _, obj, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	// Filename contains directory traversal components.
	body, ct := multipartUpload(t, "../../etc/passwd", "sensitive")

	req := authedRequest(t, http.MethodPost, "/kbs/"+kb.ID.String()+"/documents", body, userID)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// The file has no allowed extension, so we expect 422 — the important thing
	// is that the stripped base ("passwd") drives extension detection, not the
	// raw path (which would have no extension and also fail).
	// Try a traversal with a valid extension to fully exercise the sanitisation path.
	body2, ct2 := multipartUpload(t, "../../notes.txt", "safe content")
	req2 := authedRequest(t, http.MethodPost, "/kbs/"+kb.ID.String()+"/documents", body2, userID)
	req2.Header.Set("Content-Type", ct2)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201 — body: %s", w2.Code, w2.Body)
	}

	var doc document.Document
	if err := json.NewDecoder(w2.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Filename != "notes.txt" {
		t.Errorf("filename: got %q, want %q (traversal components must be stripped)", doc.Filename, "notes.txt")
	}
	if strings.Contains(doc.S3Key, "..") {
		t.Errorf("s3_key contains path traversal: %q", doc.S3Key)
	}
	// Object must be stored under the safe key.
	if _, err := obj.Get(context.TODO(), doc.S3Key); err != nil {
		t.Fatalf("s3 object not found at sanitised key: %v", err)
	}
	_ = w // suppress unused warning
}

// --- Finding 2: unbounded upload size ---

func TestDocUpload_TooLarge(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	deps.MaxUploadBytes = 512 // 512-byte cap for this test
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	// Content is larger than the cap.
	body, ct := multipartUpload(t, "notes.txt", strings.Repeat("x", 1024))

	req := authedRequest(t, http.MethodPost, "/kbs/"+kb.ID.String()+"/documents", body, userID)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d, want 413", w.Code)
	}
}

// --- Finding 3: cross-KB document access via kbID in URL ---

func TestDocGet_WrongKB(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb1, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	kb2, _ := kbRepo.Create(context.TODO(), userID, "kb2")

	// Document belongs to kb1.
	doc, _ := docRepo.Create(context.TODO(), &document.Document{
		KBID: kb1.ID, UserID: userID, Filename: "f.txt",
		S3Key: "k1", ContentType: "text/plain", Status: document.StatusPending,
	})

	// Requesting it under kb2's URL must return 404.
	req := authedRequest(t, http.MethodGet,
		"/kbs/"+kb2.ID.String()+"/documents/"+doc.ID.String(), nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-KB get: got %d, want 404", w.Code)
	}
}

func TestDocDelete_WrongKB(t *testing.T) {
	deps, kbRepo, docRepo, obj, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb1, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	kb2, _ := kbRepo.Create(context.TODO(), userID, "kb2")

	s3Key := "documents/test/cross-kb.txt"
	if err := obj.Put(context.TODO(), s3Key, bytes.NewReader([]byte("data")), 4, "text/plain"); err != nil {
		t.Fatal(err)
	}
	doc, _ := docRepo.Create(context.TODO(), &document.Document{
		KBID: kb1.ID, UserID: userID, Filename: "cross-kb.txt",
		S3Key: s3Key, ContentType: "text/plain", Status: document.StatusPending,
	})

	// Delete via kb2 must be rejected; object must remain.
	req := authedRequest(t, http.MethodDelete,
		"/kbs/"+kb2.ID.String()+"/documents/"+doc.ID.String(), nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-KB delete: got %d, want 404", w.Code)
	}
	// Object must still exist.
	if _, err := obj.Get(context.TODO(), s3Key); err != nil {
		t.Error("s3 object should not have been deleted")
	}
}

// --- Finding 4: publish error → document marked failed ---

func TestDocUpload_PublishError(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	deps.Publisher = &failPublisher{}
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	body, ct := multipartUpload(t, "notes.txt", "content")

	req := authedRequest(t, http.MethodPost, "/kbs/"+kb.ID.String()+"/documents", body, userID)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500 on publish failure", w.Code)
	}

	// The document row must exist and be marked failed.
	docs, err := docRepo.ListByKB(context.TODO(), userID, kb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document record, got %d", len(docs))
	}
	if docs[0].Status != document.StatusFailed {
		t.Errorf("status: got %q, want failed", docs[0].Status)
	}
}

func TestDocList(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	_, _ = docRepo.Create(context.TODO(), &document.Document{
		KBID: kb.ID, UserID: userID, Filename: "a.txt",
		S3Key: "k1", ContentType: "text/plain", Status: document.StatusPending,
	})
	_, _ = docRepo.Create(context.TODO(), &document.Document{
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
	if err := json.NewDecoder(w.Body).Decode(&docs); err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Errorf("count: got %d, want 2", len(docs))
	}
}

func TestDocGet(t *testing.T) {
	deps, kbRepo, docRepo, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	created, _ := docRepo.Create(context.TODO(), &document.Document{
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
	if err := json.NewDecoder(w.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.ID != created.ID {
		t.Errorf("id mismatch")
	}
}

func TestDocDelete(t *testing.T) {
	deps, kbRepo, docRepo, obj, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(context.TODO(), userID, "kb1")

	s3Key := "documents/test/file.txt"
	if err := obj.Put(context.TODO(), s3Key, bytes.NewReader([]byte("content")), 7, "text/plain"); err != nil {
		t.Fatal(err)
	}
	created, _ := docRepo.Create(context.TODO(), &document.Document{
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

	if _, err := obj.Get(context.TODO(), s3Key); err == nil {
		t.Error("expected s3 object to be deleted")
	}
	if _, err := docRepo.Get(context.TODO(), userID, created.ID); err == nil {
		t.Error("expected document row to be deleted")
	}
}

func TestDocDelete_ObjectFirst(t *testing.T) {
	deps, kbRepo, docRepo, obj, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	kb, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	s3Key := "documents/test/file2.txt"
	if err := obj.Put(context.TODO(), s3Key, bytes.NewReader([]byte("x")), 1, "text/plain"); err != nil {
		t.Fatal(err)
	}
	created, _ := docRepo.Create(context.TODO(), &document.Document{
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
	if _, err := obj.Get(context.TODO(), s3Key); err == nil {
		t.Error("s3 object should be deleted")
	}
}

func TestDocTenantIsolation(t *testing.T) {
	deps, kbRepo, docRepo, obj, _ := defaultDeps()
	router := NewRouter(deps)
	user1, user2 := uuid.New(), uuid.New()

	kb1, _ := kbRepo.Create(context.TODO(), user1, "kb1")
	s3Key := "documents/u1/file.txt"
	if err := obj.Put(context.TODO(), s3Key, bytes.NewReader([]byte("secret")), 6, "text/plain"); err != nil {
		t.Fatal(err)
	}
	doc, _ := docRepo.Create(context.TODO(), &document.Document{
		KBID: kb1.ID, UserID: user1, Filename: "file.txt",
		S3Key: s3Key, ContentType: "text/plain", Status: document.StatusPending,
	})

	req := authedRequest(t, http.MethodGet,
		"/kbs/"+kb1.ID.String()+"/documents/"+doc.ID.String(), nil, user2)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant get: got %d, want 404", w.Code)
	}

	req = authedRequest(t, http.MethodDelete,
		"/kbs/"+kb1.ID.String()+"/documents/"+doc.ID.String(), nil, user2)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant delete: got %d, want 404", w.Code)
	}
}
