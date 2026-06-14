package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// allowedContentTypes maps accepted file extensions to their MIME type.
var allowedContentTypes = map[string]string{
	".txt": "text/plain",
	".md":  "text/markdown",
}

type docHandler struct {
	kbRepo    kb.Repository
	docRepo   document.Repository
	objects   objectstore.ObjectStore
	publisher queue.Publisher
}

func registerDocRoutes(
	mux *http.ServeMux,
	kbRepo kb.Repository,
	docRepo document.Repository,
	objects objectstore.ObjectStore,
	publisher queue.Publisher,
) {
	h := &docHandler{kbRepo: kbRepo, docRepo: docRepo, objects: objects, publisher: publisher}
	mux.HandleFunc("POST /kbs/{kbID}/documents", h.upload)
	mux.HandleFunc("GET /kbs/{kbID}/documents", h.list)
	mux.HandleFunc("GET /kbs/{kbID}/documents/{docID}", h.get)
	mux.HandleFunc("DELETE /kbs/{kbID}/documents/{docID}", h.delete)
}

// upload accepts a multipart/form-data file (field "file"), stores the bytes
// in object storage first, then creates the documents row at status "pending",
// and finally publishes a DocumentUploaded event. A failure after the S3 put
// but before the row insert leaves an orphaned object — cleanup is deferred to
// a future reconciliation job.
func (h *docHandler) upload(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	kbID, ok := parseUUID(w, r.PathValue("kbID"))
	if !ok {
		return
	}

	if _, err := h.kbRepo.Get(r.Context(), userID, kbID); err != nil {
		writeError(w, http.StatusNotFound, "knowledge base not found")
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer func() { _ = file.Close() }()

	contentType, supported := contentTypeFor(header.Filename)
	if !supported {
		writeError(w, http.StatusUnprocessableEntity, "unsupported file type; accepted: .txt, .md")
		return
	}

	buf, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read file")
		return
	}

	s3Key := fmt.Sprintf("documents/%s/%s/%s/%s", userID, kbID, uuid.New(), header.Filename)

	if err := h.objects.Put(r.Context(), s3Key, bytes.NewReader(buf), int64(len(buf)), contentType); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store file")
		return
	}

	doc, err := h.docRepo.Create(r.Context(), &document.Document{
		KBID:        kbID,
		UserID:      userID,
		Filename:    header.Filename,
		S3Key:       s3Key,
		ContentType: contentType,
		Status:      document.StatusPending,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create document record")
		return
	}

	_ = h.publisher.PublishDocumentUploaded(r.Context(), queue.DocumentUploaded{DocumentID: doc.ID})

	writeJSON(w, http.StatusCreated, doc)
}

func (h *docHandler) list(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	kbID, ok := parseUUID(w, r.PathValue("kbID"))
	if !ok {
		return
	}

	if _, err := h.kbRepo.Get(r.Context(), userID, kbID); err != nil {
		writeError(w, http.StatusNotFound, "knowledge base not found")
		return
	}

	docs, err := h.docRepo.ListByKB(r.Context(), userID, kbID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list documents")
		return
	}

	if docs == nil {
		docs = []*document.Document{}
	}
	writeJSON(w, http.StatusOK, docs)
}

func (h *docHandler) get(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	docID, ok := parseUUID(w, r.PathValue("docID"))
	if !ok {
		return
	}

	doc, err := h.docRepo.Get(r.Context(), userID, docID)
	if err != nil {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}

	writeJSON(w, http.StatusOK, doc)
}

// delete removes the S3 object first, then the database row.
// If object deletion succeeds but row deletion fails, a harmless orphan
// (file without a row) remains — preferable to a broken pointer (row without
// a file) that would surface as a runtime error on access.
func (h *docHandler) delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	docID, ok := parseUUID(w, r.PathValue("docID"))
	if !ok {
		return
	}

	doc, err := h.docRepo.Get(r.Context(), userID, docID)
	if err != nil {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}

	if err := h.objects.Delete(r.Context(), doc.S3Key); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove object")
		return
	}

	if err := h.docRepo.Delete(r.Context(), userID, docID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete document record")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// contentTypeFor returns the MIME type for the given filename based on its
// extension, and whether the extension is in the accepted set.
func contentTypeFor(filename string) (string, bool) {
	ext := strings.ToLower(path.Ext(filename))
	ct, ok := allowedContentTypes[ext]
	return ct, ok
}
