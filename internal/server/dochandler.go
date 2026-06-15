package server

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

const defaultMaxUploadBytes int64 = 32 << 20 // 32 MiB

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
	maxUpload int64
}

func registerDocRoutes(
	mux *http.ServeMux,
	kbRepo kb.Repository,
	docRepo document.Repository,
	objects objectstore.ObjectStore,
	publisher queue.Publisher,
	maxUploadBytes int64,
) {
	if maxUploadBytes <= 0 {
		maxUploadBytes = defaultMaxUploadBytes
	}
	h := &docHandler{
		kbRepo:    kbRepo,
		docRepo:   docRepo,
		objects:   objects,
		publisher: publisher,
		maxUpload: maxUploadBytes,
	}
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
		writeKBError(w, err)
		return
	}

	// Enforce a hard cap on the total request body before parsing. This
	// prevents disk exhaustion and the subsequent io.ReadAll heap spike.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUpload)
	if err := r.ParseMultipartForm(h.maxUpload); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds %d byte limit", h.maxUpload))
			return
		}
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer func() { _ = file.Close() }()

	// Strip directory components from the filename to prevent path traversal
	// in the S3 key (e.g. "../../etc/passwd" → "passwd").
	filename := path.Base(header.Filename)
	if filename == "." || filename == "" {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	contentType, supported := contentTypeFor(filename)
	if !supported {
		writeError(w, http.StatusUnprocessableEntity, "unsupported file type; accepted: .txt, .md")
		return
	}

	s3Key := fmt.Sprintf("documents/%s/%s/%s/%s", userID, kbID, uuid.New(), filename)

	// Stream directly from the multipart part — no io.ReadAll buffering needed.
	if err := h.objects.Put(r.Context(), s3Key, file, header.Size, contentType); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store file")
		return
	}

	doc, err := h.docRepo.Create(r.Context(), &document.Document{
		KBID:        kbID,
		UserID:      userID,
		Filename:    filename,
		S3Key:       s3Key,
		ContentType: contentType,
		Status:      document.StatusPending,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create document record")
		return
	}

	if err := h.publisher.PublishDocumentUploaded(r.Context(), queue.DocumentUploaded{DocumentID: doc.ID, UserID: userID}); err != nil {
		// Mark failed so the caller knows processing will not happen. The S3
		// object and DB row are retained — the reconciliation job can retry.
		_ = h.docRepo.UpdateStatus(r.Context(), userID, doc.ID, document.StatusFailed)
		writeError(w, http.StatusInternalServerError, "failed to enqueue document for processing")
		return
	}

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
		writeKBError(w, err)
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

	kbID, ok := parseUUID(w, r.PathValue("kbID"))
	if !ok {
		return
	}

	docID, ok := parseUUID(w, r.PathValue("docID"))
	if !ok {
		return
	}

	doc, err := h.docRepo.Get(r.Context(), userID, docID)
	if err != nil {
		writeDocError(w, err)
		return
	}

	// Verify the document belongs to the KB named in the URL so that a user
	// cannot access documents across their own KBs by guessing IDs.
	if doc.KBID != kbID {
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

	kbID, ok := parseUUID(w, r.PathValue("kbID"))
	if !ok {
		return
	}

	docID, ok := parseUUID(w, r.PathValue("docID"))
	if !ok {
		return
	}

	doc, err := h.docRepo.Get(r.Context(), userID, docID)
	if err != nil {
		writeDocError(w, err)
		return
	}

	if doc.KBID != kbID {
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

// writeDocError translates a document.Repository error to the appropriate HTTP status.
func writeDocError(w http.ResponseWriter, err error) {
	if errors.Is(err, document.ErrNotFound) {
		writeError(w, http.StatusNotFound, "document not found")
	} else {
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
