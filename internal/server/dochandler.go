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

	"github.com/kunalpednekar/dumpster/internal/canonical"
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
	jobs              queue.JobStatusReader  // nil when per-document progress is not available
	manifest          manifest.Repository    // nil when manifest not yet available
	canonical         canonical.Repository   // nil when canonicalization not yet available
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
	jobs queue.JobStatusReader,
	manifestRepo manifest.Repository,
	canonicalRepo canonical.Repository,
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
		jobs:              jobs,
		manifest:          manifestRepo,
		canonical:         canonicalRepo,
		instruments:       instruments,
		maxUpload:         maxUploadBytes,
		maxDocsPerSession: maxDocsPerSession,
	}
	mux.HandleFunc("POST /kbs/{kbID}/documents", h.upload)
	mux.HandleFunc("GET /kbs/{kbID}/documents", h.list)
	mux.HandleFunc("GET /kbs/{kbID}/documents/{docID}", h.get)
	mux.HandleFunc("GET /kbs/{kbID}/documents/{docID}/content", h.content)
	mux.HandleFunc("DELETE /kbs/{kbID}/documents/{docID}", h.delete)
	mux.HandleFunc("POST /kbs/{kbID}/documents/{docID}/retry", h.retry)
	mux.HandleFunc("POST /kbs/{kbID}/documents/{docID}/retry-entity-extraction", h.retryEntityExtraction)
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
		SizeBytes:   header.Size,
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

// hasActiveJobForDocument reports whether documentID currently has any
// pending or processing background job -- e.g. an entity-extraction job
// that started before this document's own ingestion job failed and was
// dead-lettered, now that entity extraction can start before a document
// reaches Indexed (see internal/worker/regionhandler.go's process()).
// ActiveJobsForDocuments already excludes dead-lettered jobs (a "failed"
// job's row is never deleted, but the query filters to pending/processing
// only), so a document whose entity extraction permanently failed is
// never locked out of delete/retry here. Returns false, nil (not an
// error) when no JobStatusReader is configured for this deployment.
func (h *docHandler) hasActiveJobForDocument(ctx context.Context, userID, documentID uuid.UUID) (bool, error) {
	if h.jobs == nil {
		return false, nil
	}
	active, err := h.jobs.ActiveJobsForDocuments(ctx, userID, []uuid.UUID{documentID})
	if err != nil {
		return false, err
	}
	return len(active[documentID]) > 0, nil
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

	page := paginateDocs(docs, limit, after)
	writeJSON(w, http.StatusOK, h.enrichPage(r.Context(), userID, page))
}

// docListItem is a document.Document enriched with per-document ingestion
// progress for the document list endpoint. See docDetailResponse for the
// same enrichment pattern applied to the single-document endpoint.
type docListItem struct {
	*document.Document
	// Progress is set for every status except failed (a dead-lettered
	// document has nothing left to report). It stays set forever once a
	// document is fully indexed -- see documentProgress.ActiveStages --
	// so a caller can show ingestion history, not just live progress.
	Progress *documentProgress `json:"progress,omitempty"`
}

// documentProgress is the staged-checklist view of ingestion progress a
// document upload UI can render directly: the full ordered list of stages
// this document will pass through, and which one(s) are active right now.
type documentProgress struct {
	Stages []queue.DisplayStage `json:"stages"`
	// ActiveStages holds every DisplayStage.Key genuinely in progress
	// right now, in canonical stage order -- more than one when e.g. a
	// document's own indexing job is still embedding while its (already
	// started) entity-extraction job is also running (see
	// worker.RegionClassificationHandler.process's doc). Empty when the
	// document is queued but no job has started processing it yet (i.e.
	// still on the first stage, not yet picked up by a worker).
	// Permanently [queue.StageKeyComplete] once the whole pipeline --
	// including background entity extraction -- has finished.
	ActiveStages []string `json:"active_stages,omitempty"`
}

// documentPageResponse mirrors DocumentPage but carries the enriched
// docListItem in place of a raw *document.Document.
type documentPageResponse struct {
	Items      []*docListItem `json:"items"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// enrichPage attaches progress info to a page of documents in a single
// batched ActiveJobsForDocuments lookup, rather than one query per
// document.
func (h *docHandler) enrichPage(ctx context.Context, userID uuid.UUID, page DocumentPage) documentPageResponse {
	resp := documentPageResponse{NextCursor: page.NextCursor, Items: make([]*docListItem, len(page.Items))}

	if h.jobs == nil {
		for i, d := range page.Items {
			resp.Items[i] = &docListItem{Document: d}
		}
		return resp
	}

	ids := make([]uuid.UUID, len(page.Items))
	for i, d := range page.Items {
		ids[i] = d.ID
	}
	activeByDoc, err := h.jobs.ActiveJobsForDocuments(ctx, userID, ids)
	if err != nil {
		slog.Error("failed to load job statuses for document progress", "err", err)
	}

	for i, d := range page.Items {
		item := &docListItem{Document: d}
		// A failed (dead-lettered) document has nothing left to show --
		// there's no in-flight or completed pipeline to report on. Every
		// other status gets a checklist, including "indexed": that only
		// means region classification + embedding finished, while
		// entity_extraction/edge_extraction/canonicalization keep running
		// as a background pipeline afterward (document.Status has no
		// value for "indexed but entities still extracting"). Once that
		// pipeline is done too, the document sits permanently on
		// queue.StageKeyComplete rather than losing its checklist --
		// callers use this to show a document's ingestion history, not
		// just its live progress.
		if d.Status != document.StatusFailed {
			hasRegionClassification := regionClassificationTypes[d.ContentType]
			progress := &documentProgress{
				Stages: queue.StagesForDocument(hasRegionClassification),
			}
			switch activeStages := queue.ActiveStageKeys(hasRegionClassification, activeByDoc[d.ID]); {
			case len(activeStages) > 0:
				progress.ActiveStages = activeStages
			case d.Status == document.StatusIndexed:
				progress.ActiveStages = []string{queue.StageKeyComplete}
			}
			item.Progress = progress
		}
		resp.Items[i] = item
	}
	return resp
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

	// A document actively being indexed has an in-flight worker job holding
	// its own reference to the S3 object and this row for the duration of
	// Handle(); nothing in the worker re-checks either mid-run. Deleting here
	// would either dead-letter that job against a vanished document or let it
	// write chunks for a document that no longer exists.
	if doc.Status == document.StatusProcessing {
		writeError(w, http.StatusConflict, "document is being indexed and cannot be deleted yet")
		return
	}

	// Entity extraction can now start before a document reaches Indexed
	// (see internal/worker/regionhandler.go's process()), so it can
	// still be genuinely active even once doc.Status has moved past
	// Processing -- including Indexed or Failed. Deleting
	// out from under it would cascade away the entities row it's about
	// to write into, or leave it reading chunk rows that no longer exist.
	hasActiveJob, err := h.hasActiveJobForDocument(r.Context(), userID, docID)
	if err != nil {
		h.recordDelete(r.Context(), "failure")
		writeError(w, http.StatusInternalServerError, "failed to check for an active background job")
		return
	}
	if hasActiveJob {
		writeError(w, http.StatusConflict, "document has an active background job and cannot be deleted yet")
		return
	}

	// Reverse this document's contribution to canonical entity stats before
	// its entities row are cascade-deleted by the document delete below —
	// the mention -> canonical linkage DecrementForDocument needs to
	// reverse it is gone once that cascade runs. Mirrors EntityHandler's
	// re-extraction path, the only other place entities disappear.
	if h.canonical != nil {
		if err := h.canonical.DecrementForDocument(r.Context(), userID, docID); err != nil {
			h.recordDelete(r.Context(), "failure")
			writeError(w, http.StatusInternalServerError, "failed to update canonical entities")
			return
		}
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

// retry re-enqueues a dead-lettered (status "failed") document for
// processing, reusing the object already in storage — no re-upload needed.
// Only a failed document can be retried; anything else is a 409, since
// retrying a document that's pending/processing/indexed either races the
// in-flight job or silently re-processes an already-good index.
func (h *docHandler) retry(w http.ResponseWriter, r *http.Request) {
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

	if doc.Status != document.StatusFailed {
		writeError(w, http.StatusConflict, "only a failed document can be retried")
		return
	}

	// A document can fail (e.g. an embed error) after entity extraction
	// has already started for it (see internal/worker/regionhandler.go's
	// process()) -- retrying here re-chunks the document from scratch,
	// which must not race that still-active job reading the old chunk
	// rows out from under it.
	hasActiveJob, err := h.hasActiveJobForDocument(r.Context(), userID, docID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check for an active background job")
		return
	}
	if hasActiveJob {
		writeError(w, http.StatusConflict, "document has an active background job and cannot be retried yet")
		return
	}

	if err := h.docRepo.UpdateStatus(r.Context(), userID, docID, document.StatusPending); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reset document status")
		return
	}

	var publishErr error
	if regionClassificationTypes[doc.ContentType] {
		publishErr = h.publisher.PublishRegionClassification(r.Context(), queue.RegionClassificationRequested{
			DocumentID: doc.ID, UserID: userID,
		})
	} else {
		publishErr = h.publisher.PublishDocumentUploaded(r.Context(), queue.DocumentUploaded{
			DocumentID: doc.ID, UserID: userID,
		})
	}
	if publishErr != nil {
		_ = h.docRepo.UpdateStatus(r.Context(), userID, docID, document.StatusFailed)
		writeError(w, http.StatusInternalServerError, "failed to enqueue document for reprocessing")
		return
	}

	updated, err := h.docRepo.Get(r.Context(), userID, docID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load updated document")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// retryEntityExtraction handles POST
// /kbs/{kbID}/documents/{docID}/retry-entity-extraction: re-publishes just
// the entity-extraction stage for an already-indexed document, without
// touching document status or re-running chunking/embedding.
//
// This exists as a distinct, narrower operation from retry (which requires
// StatusFailed and re-runs the whole pipeline from upload): entity
// extraction can fail or need re-running (a changed ENTITY_TYPES list, an
// extractor code/image change under test) independent of the document's
// own indexing having succeeded, and re-chunking/re-embedding an already-
// correctly-indexed document to test an unrelated extraction change is
// pure wasted latency -- observed in practice at over 2 minutes for
// embedding alone on a modest document.
//
// EntityHandler's own idempotent cleanup (job.Attempts == 0: reverse this
// document's canonical entity contribution, delete its existing entities)
// makes this safe to call repeatedly -- each run fully replaces the last,
// it never accumulates duplicates.
func (h *docHandler) retryEntityExtraction(w http.ResponseWriter, r *http.Request) {
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

	if doc.Status != document.StatusIndexed {
		writeError(w, http.StatusConflict, "only an indexed document has chunks to extract entities from")
		return
	}

	hasActiveJob, err := h.hasActiveJobForDocument(r.Context(), userID, docID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check for an active background job")
		return
	}
	if hasActiveJob {
		writeError(w, http.StatusConflict, "document has an active background job and cannot be retried yet")
		return
	}

	if err := h.publisher.PublishEntityExtraction(r.Context(), queue.EntityExtractionRequested{
		DocumentID: doc.ID, UserID: userID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enqueue entity extraction")
		return
	}

	writeJSON(w, http.StatusOK, doc)
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
