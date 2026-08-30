package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/inquiry"
	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/search"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

type searchHandler struct {
	kbRepo      kb.Repository
	docRepo     document.Repository
	searcher    search.Searcher
	inquiryRepo inquiry.Repository     // nil disables Inquiry history persistence
	instruments *telemetry.Instruments // nil when metrics are not configured
}

func registerSearchRoutes(mux *http.ServeMux, kbRepo kb.Repository, docRepo document.Repository, searcher search.Searcher, inquiryRepo inquiry.Repository, instruments *telemetry.Instruments) {
	h := &searchHandler{kbRepo: kbRepo, docRepo: docRepo, searcher: searcher, inquiryRepo: inquiryRepo, instruments: instruments}
	mux.HandleFunc("POST /kbs/{id}/search", h.search)
}

// search handles POST /kbs/{id}/search. It verifies the knowledge base belongs
// to the requesting user, then streams the answer back as Server-Sent
// Events: a retrieved_files event (the ranked retrieval set, before
// generation begins), one delta event per generated text chunk, and a
// final done event carrying the parsed summary and citations. See
// StreamEvent's doc for why the wire format splits it this way.
//
// The 200 + text/event-stream response only commits once the first event is
// ready to send (see startStream) — an error that happens before any event
// fires (e.g. retrieval failing) still gets a normal JSON error response
// with a real status code, since nothing has been written yet. An error
// after streaming has started can no longer change the status code, so it's
// signaled with an error event inside the still-open stream instead.
func (h *searchHandler) search(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	kbID, ok := parseUUID(w, r.PathValue("id"))
	if !ok {
		return
	}

	if _, err := h.kbRepo.Get(r.Context(), userID, kbID); err != nil {
		writeKBError(w, err)
		return
	}

	var body struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}

	fileNames := h.fileNames(r.Context(), userID, kbID)
	inq := h.persistUserMessage(r.Context(), userID, kbID, body.Query)
	flusher, _ := w.(http.Flusher)
	streamStarted := false
	startStream := func() {
		if streamStarted {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		streamStarted = true
	}

	started := time.Now()
	result, err := h.searcher.SearchStream(r.Context(), kbID, body.Query, func(e search.StreamEvent) {
		startStream()
		writeSSE(w, flusher, toWireEvent(e, fileNames))
	})
	h.recordSearchLatency(r.Context(), started)

	if err != nil {
		if !streamStarted {
			writeError(w, http.StatusInternalServerError, "search failed")
			return
		}
		writeSSE(w, flusher, sseEvent{name: "error", data: errorEvent{Error: "search failed"}})
		return
	}

	h.persistAssistantMessage(r.Context(), userID, inq, result, fileNames)

	startStream() // defensive: SearchStream succeeding with zero events never happens today, but this keeps the response well-formed if it ever did.
	writeSSE(w, flusher, sseEvent{name: "done", data: doneEvent{
		Summary:   result.Summary,
		Citations: buildCitations(result.Citations, fileNames),
	}})
}

// persistUserMessage resolves the researcher's single Inquiry for this KB
// (creating it on first use) and records query as its next turn, returning
// the Inquiry so persistAssistantMessage can append the matching answer to
// it. Returns nil when persistence is disabled (h.inquiryRepo == nil) or
// fails outright — Inquiry history is a convenience layered on top of
// search, not a dependency search itself needs to succeed, so a failure
// here is logged and otherwise swallowed rather than failing the request.
func (h *searchHandler) persistUserMessage(ctx context.Context, userID, kbID uuid.UUID, query string) *inquiry.Inquiry {
	if h.inquiryRepo == nil {
		return nil
	}
	inq, err := h.inquiryRepo.GetOrCreate(ctx, userID, kbID)
	if err != nil {
		slog.Error("inquiry: get or create failed", "kb_id", kbID, "err", err)
		return nil
	}
	if _, err := h.inquiryRepo.AppendMessage(ctx, userID, &inquiry.Message{
		InquiryID: inq.ID, KBID: kbID, Role: inquiry.RoleUser, Content: query,
	}); err != nil {
		slog.Error("inquiry: persist user message failed", "inquiry_id", inq.ID, "err", err)
	}
	return inq
}

// persistAssistantMessage records result as the next turn in inq, a no-op
// when inq is nil (persistence disabled or persistUserMessage already
// failed for this request — there's no inquiry to attach an answer to).
// Each citation's FileName is snapshotted from fileNames, the same
// request-time lookup used for the live response: unlike a live citation,
// a persisted one may be read back long after its source document was
// renamed or deleted, so FileName has to be captured now rather than
// resolved again later.
func (h *searchHandler) persistAssistantMessage(ctx context.Context, userID uuid.UUID, inq *inquiry.Inquiry, result search.Result, fileNames map[uuid.UUID]string) {
	if inq == nil {
		return
	}
	citations := make([]inquiry.Citation, len(result.Citations))
	for i, c := range result.Citations {
		citations[i] = inquiry.Citation{
			Number:     c.Number,
			DocumentID: c.DocumentID,
			ChunkID:    c.ChunkID,
			CharStart:  c.CharStart,
			CharEnd:    c.CharEnd,
			FileName:   fileNames[c.DocumentID],
			PageNumber: c.PageNumber,
		}
		if c.BoundingBox != nil {
			citations[i].BoundingBox = &inquiry.BoundingBox{
				X0: c.BoundingBox.X0, Y0: c.BoundingBox.Y0, X1: c.BoundingBox.X1, Y1: c.BoundingBox.Y1,
			}
		}
	}
	_, err := h.inquiryRepo.AppendMessage(ctx, userID, &inquiry.Message{
		InquiryID:          inq.ID,
		KBID:               inq.KBID,
		Role:               inquiry.RoleAssistant,
		Content:            result.Summary,
		Citations:          citations,
		RetrievedDocuments: result.RetrievedDocuments,
	})
	if err != nil {
		slog.Error("inquiry: persist assistant message failed", "inquiry_id", inq.ID, "err", err)
	}
}

// fileNames resolves every citation's DocumentID to its filename in one
// query, rather than one Get call per citation. A document that can't be
// resolved (e.g. deleted between indexing and query) is simply absent from
// the map — buildCitations/buildRetrievedFiles fall back to an empty file
// name rather than failing the whole response over one stale reference.
func (h *searchHandler) fileNames(ctx context.Context, userID, kbID uuid.UUID) map[uuid.UUID]string {
	docs, err := h.docRepo.ListByKB(ctx, userID, kbID)
	if err != nil {
		return nil
	}
	names := make(map[uuid.UUID]string, len(docs))
	for _, d := range docs {
		names[d.ID] = d.Filename
	}
	return names
}

// recordSearchLatency records SearchLatency for the full search request
// regardless of outcome, since end-to-end latency is meaningful whether or
// not the search ultimately succeeded.
func (h *searchHandler) recordSearchLatency(ctx context.Context, started time.Time) {
	if h.instruments == nil {
		return
	}
	h.instruments.SearchLatency.Record(ctx, float64(time.Since(started).Microseconds())/1000)
}

// sseEvent is one Server-Sent Event frame: "event: <name>\ndata: <json>\n\n".
type sseEvent struct {
	name string
	data any
}

// writeSSE serializes data as JSON and writes one SSE frame, flushing
// immediately so the client sees it as soon as it's written rather than
// waiting for Go's response buffering to fill up.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, e sseEvent) {
	payload, err := json.Marshal(e.data)
	if err != nil {
		payload = []byte(`{"error":"internal encoding error"}`)
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.name, payload)
	if flusher != nil {
		flusher.Flush()
	}
}

// retrievedFilesEvent is the JSON payload of a "retrieved_files" SSE event.
type retrievedFilesEvent struct {
	RetrievedFiles []RetrievedFileResponse `json:"retrieved_files"`
}

// deltaEvent is the JSON payload of a "delta" SSE event: one chunk of
// generated answer text.
type deltaEvent struct {
	Text string `json:"text"`
}

// doneEvent is the JSON payload of the final "done" SSE event: the parsed
// summary and its citations. RetrievedFiles isn't repeated here — it
// already went out as its own event before generation began.
type doneEvent struct {
	Summary   string             `json:"summary"`
	Citations []CitationResponse `json:"citations"`
}

// errorEvent is the JSON payload of an "error" SSE event, sent when the
// search fails after streaming has already started (so the status code can
// no longer change).
type errorEvent struct {
	Error string `json:"error"`
}

// toWireEvent converts a search.StreamEvent into the sseEvent that gets
// written to the client.
func toWireEvent(e search.StreamEvent, fileNames map[uuid.UUID]string) sseEvent {
	switch e.Type {
	case search.EventRetrievedFiles:
		return sseEvent{name: "retrieved_files", data: retrievedFilesEvent{RetrievedFiles: buildRetrievedFiles(e.RetrievedDocuments, fileNames)}}
	default: // search.EventDelta
		return sseEvent{name: "delta", data: deltaEvent{Text: e.Delta}}
	}
}

// RetrievedFileResponse names one file from the ranked retrieval set.
type RetrievedFileResponse struct {
	DocumentID string `json:"document_id"`
	FileName   string `json:"file_name"`
}

// CitationResponse is the JSON shape of a single citation. Number is the
// 1-indexed [N] marker this citation corresponds to in Summary — clients
// must match a marker to a citation by Number, not by array position, since
// this slice is sparse whenever the model didn't cite every numbered chunk.
//
// FileName + Locator (when present) are the entire citation identity: the
// client groups adjacent markers by FileName and shows a page locator when
// one applies, mirroring how search engines cite the source page rather
// than a byte range within it. There is no chunk-text field — the raw
// source span is no longer surfaced to clients (see CitationMarker on the
// frontend), only which file (and page) it came from.
type CitationResponse struct {
	Number     int              `json:"number"`
	DocumentID string           `json:"document_id"`
	ChunkID    string           `json:"chunk_id"`
	CharStart  int              `json:"char_start"`
	CharEnd    int              `json:"char_end"`
	FileName   string           `json:"file_name"`
	Locator    *CitationLocator `json:"locator"`
}

// CitationLocator narrows a citation within its file. Value is an int today
// (a page number); a future video timestamp locator would add a new Type
// rather than requiring a shape change here.
type CitationLocator struct {
	Type  string `json:"type"`
	Value int    `json:"value"`
}

func buildCitations(cs []search.Citation, fileNames map[uuid.UUID]string) []CitationResponse {
	citations := make([]CitationResponse, len(cs))
	for i, c := range cs {
		citations[i] = CitationResponse{
			Number:     c.Number,
			DocumentID: c.DocumentID.String(),
			ChunkID:    c.ChunkID.String(),
			CharStart:  c.CharStart,
			CharEnd:    c.CharEnd,
			FileName:   fileNames[c.DocumentID],
			Locator:    citationLocator(c),
		}
	}
	return citations
}

func buildRetrievedFiles(docIDs []uuid.UUID, fileNames map[uuid.UUID]string) []RetrievedFileResponse {
	retrievedFiles := make([]RetrievedFileResponse, len(docIDs))
	for i, docID := range docIDs {
		retrievedFiles[i] = RetrievedFileResponse{
			DocumentID: docID.String(),
			FileName:   fileNames[docID],
		}
	}
	return retrievedFiles
}

// citationLocator returns the modality-native locator for c, or nil when
// none applies (plain text/markdown chunks, or a PDF chunk missing a page
// number). PDF is the only source of a locator today; a future video
// timestamp locator would add another case here rather than changing the
// CitationLocator shape.
func citationLocator(c search.Citation) *CitationLocator {
	if c.PageNumber == nil {
		return nil
	}
	return &CitationLocator{Type: "page", Value: *c.PageNumber}
}
