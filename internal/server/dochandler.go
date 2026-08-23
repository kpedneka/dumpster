package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/manifest"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

const (
	defaultMaxUploadBytes    int64 = 32 << 20 // 32 MiB
	defaultMaxDocsPerSession       = 20
)

// allowedContentTypes maps accepted file extensions to their MIME type.
// PDF and image types are routed through the region-classification pipeline;
// text/markdown continues through the plain-text chunking pipeline.
var allowedContentTypes = map[string]string{
	".txt":  "text/plain",
	".md":   "text/markdown",
	".pdf":  "application/pdf",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
}

// regionClassificationTypes is the subset of allowedContentTypes that need
// the region-classification pipeline (pdfplumber + VLM) instead of the
// plain-text chunking pipeline.
var regionClassificationTypes = map[string]bool{
	"application/pdf": true,
	"image/png":       true,
	"image/jpeg":      true,
}

type docHandler struct {
	kbRepo            kb.Repository
	docRepo           document.Repository
	objects           objectstore.ObjectStore
	publisher         queue.Publisher
	manifest          manifest.Repository    // nil when manifest not yet available
	instruments       *telemetry.Instruments // nil when metrics are not configured
	maxUpload         int64
	maxDocsPerSession int
}

func registerDocRoutes(
	mux *http.ServeMux,
	kbRepo kb.Repository,
	docRepo document.Repository,
	objects objectstore.ObjectStore,
	publisher queue.Publisher,
	manifestRepo manifest.Repository,
	instruments *telemetry.Instruments,
	maxUploadBytes int64,
	maxDocsPerSession int,
) {
	if maxUploadBytes <= 0 {
		maxUploadBytes = defaultMaxUploadBytes
	}
	if maxDocsPerSession <= 0 {
		maxDocsPerSession = defaultMaxDocsPerSession
	}
	h := &docHandler{
		kbRepo:            kbRepo,
		docRepo:           docRepo,
		objects:           objects,
		publisher:         publisher,
		manifest:          manifestRepo,
		instruments:       instruments,
		maxUpload:         maxUploadBytes,
		maxDocsPerSession: maxDocsPerSession,
	}
	mux.HandleFunc("POST /kbs/{kbID}/documents", h.upload)
	mux.HandleFunc("GET /kbs/{kbID}/documents", h.list)
	mux.HandleFunc("GET /kbs/{kbID}/documents/{docID}", h.get)
	mux.HandleFunc("GET /kbs/{kbID}/documents/{docID}/content", h.content)
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

	// Enforce the per-session document cap before consuming the request body.
	existing, err := h.docRepo.ListByUserID(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check document quota")
		return
	}
	if len(existing) >= h.maxDocsPerSession {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("session document limit of %d reached", h.maxDocsPerSession))
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
		writeError(w, http.StatusUnprocessableEntity, "unsupported file type; accepted: .txt, .md, .pdf, .png, .jpg")
		return
	}

	s3Key := fmt.Sprintf("documents/%s/%s/%s/%s", userID, kbID, uuid.New(), filename)

	// Stream directly from the multipart part — no io.ReadAll buffering needed.
	if err := h.objects.Put(r.Context(), s3Key, file, header.Size, contentType); err != nil {
		slog.Error("s3 put failed", "key", s3Key, "err", err)
		h.recordUpload(r.Context(), "failure")
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
		h.recordUpload(r.Context(), "failure")
		writeError(w, http.StatusInternalServerError, "failed to create document record")
		return
	}

	var publishErr error
	if regionClassificationTypes[contentType] {
		publishErr = h.publisher.PublishRegionClassification(r.Context(), queue.RegionClassificationRequested{
			DocumentID: doc.ID, UserID: userID,
		})
	} else {
		publishErr = h.publisher.PublishDocumentUploaded(r.Context(), queue.DocumentUploaded{
			DocumentID: doc.ID, UserID: userID,
		})
	}
	if publishErr != nil {
		// Mark failed so the caller knows processing will not happen. The S3
		// object and DB row are retained — the reconciliation job can retry.
		_ = h.docRepo.UpdateStatus(r.Context(), userID, doc.ID, document.StatusFailed)
		h.recordUpload(r.Context(), "failure")
		writeError(w, http.StatusInternalServerError, "failed to enqueue document for processing")
		return
	}

	h.recordUpload(r.Context(), "success")
	writeJSON(w, http.StatusCreated, doc)
}

// recordUpload increments DocumentsUploadedTotal for the given outcome, once
// an upload has actually begun persisting. Call sites upstream of the first
// h.objects.Put in upload() (auth, validation, quota) must not call this —
// those are request rejections, not upload attempts.
func (h *docHandler) recordUpload(ctx context.Context, outcome string) {
	if h.instruments == nil {
		return
	}
	h.instruments.DocumentsUploadedTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("outcome", outcome)),
	)
}

// recordDelete increments DocumentsDeletedTotal for the given outcome, once a
// deletion has actually begun (the document was found and its removal was
// attempted). See recordUpload for why lookup/auth failures don't count.
func (h *docHandler) recordDelete(ctx context.Context, outcome string) {
	if h.instruments == nil {
		return
	}
	h.instruments.DocumentsDeletedTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("outcome", outcome)),
	)
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

	limit, after := parsePagination(r)

	docs, err := h.docRepo.ListByKB(r.Context(), userID, kbID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list documents")
		return
	}

	writeJSON(w, http.StatusOK, paginateDocs(docs, limit, after))
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

	// Enrich the response with the ingestion manifest summary when available.
	type docDetailResponse struct {
		*document.Document
		// ManifestSummary is set for PDF/image documents once region
		// classification has run. Omitted (nil) for text/markdown documents
		// and before classification completes.
		ManifestSummary *manifestSummary `json:"manifest_summary,omitempty"`
	}

	resp := &docDetailResponse{Document: doc}
	if h.manifest != nil {
		regions, _ := h.manifest.ListByDocument(r.Context(), userID, docID)
		if len(regions) > 0 {
			var indexed, skipped, failed int
			for _, reg := range regions {
				switch reg.Status {
				case manifest.StatusIndexed:
					indexed++
				case manifest.StatusSkipped:
					skipped++
				case manifest.StatusFailed:
					failed++
				}
			}
			resp.ManifestSummary = &manifestSummary{
				Indexed: indexed, Skipped: skipped, Failed: failed,
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

type manifestSummary struct {
	Indexed int `json:"indexed"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
}

// content streams the raw text of a document from object storage. Only
// .txt/.md files are ever accepted at upload, so the body is always safe to
// serve as text/plain.
func (h *docHandler) content(w http.ResponseWriter, r *http.Request) {
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

	rc, err := h.objects.Get(r.Context(), doc.S3Key)
	if err != nil {
		slog.Error("s3 get failed", "key", doc.S3Key, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load document content")
		return
	}
	defer func() { _ = rc.Close() }()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
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
		h.recordDelete(r.Context(), "failure")
		writeError(w, http.StatusInternalServerError, "failed to remove object")
		return
	}

	if err := h.docRepo.Delete(r.Context(), userID, docID); err != nil {
		h.recordDelete(r.Context(), "failure")
		writeError(w, http.StatusInternalServerError, "failed to delete document record")
		return
	}

	h.recordDelete(r.Context(), "success")
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
