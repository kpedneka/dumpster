package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/search"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

type searchHandler struct {
	kbRepo      kb.Repository
	docRepo     document.Repository
	searcher    search.Searcher
	instruments *telemetry.Instruments // nil when metrics are not configured
}

func registerSearchRoutes(mux *http.ServeMux, kbRepo kb.Repository, docRepo document.Repository, searcher search.Searcher, instruments *telemetry.Instruments) {
	h := &searchHandler{kbRepo: kbRepo, docRepo: docRepo, searcher: searcher, instruments: instruments}
	mux.HandleFunc("POST /kbs/{id}/search", h.search)
}

// search handles POST /kbs/{id}/search. It verifies the knowledge base belongs
// to the requesting user before delegating to the search service.
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

	started := time.Now()
	result, err := h.searcher.Search(r.Context(), kbID, body.Query)
	h.recordSearchLatency(r.Context(), started)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}

	writeJSON(w, http.StatusOK, toSearchResponse(result, h.fileNames(r.Context(), userID, kbID)))
}

// fileNames resolves every citation's DocumentID to its filename in one
// query, rather than one Get call per citation. A document that can't be
// resolved (e.g. deleted between indexing and query) is simply absent from
// the map — toSearchResponse falls back to an empty file name rather than
// failing the whole response over one stale reference.
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

// SearchResponse is the JSON shape returned by the search endpoint.
type SearchResponse struct {
	Summary   string             `json:"summary"`
	Citations []CitationResponse `json:"citations"`
	// RetrievedFiles is every file the fused top-k retrieval surfaced,
	// ranked, independent of which subset ended up cited inline in Summary
	// — the relevant-files surface renders this list, not just Citations.
	RetrievedFiles []RetrievedFileResponse `json:"retrieved_files"`
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
// FileName + Locator (when present) are the primary citation identity,
// mirroring how search engines cite the source page rather than a byte
// range within it. Text remains the drill-down evidence behind that
// citation, not the primary display.
type CitationResponse struct {
	Number     int    `json:"number"`
	DocumentID string `json:"document_id"`
	ChunkID    string `json:"chunk_id"`
	CharStart  int    `json:"char_start"`
	CharEnd    int    `json:"char_end"`
	// Text is the cited chunk's own extracted content, served directly so
	// clients never need to re-fetch and slice the original document —
	// slicing by CharStart/CharEnd only reproduces the right span for
	// text/markdown documents, not PDF/image region-derived chunks.
	Text     string           `json:"text"`
	FileName string           `json:"file_name"`
	Locator  *CitationLocator `json:"locator"`
}

// CitationLocator narrows a citation within its file. Value is an int today
// (a page number); a future video timestamp locator would add a new Type
// rather than requiring a shape change here.
type CitationLocator struct {
	Type  string `json:"type"`
	Value int    `json:"value"`
}

func toSearchResponse(r search.Result, fileNames map[uuid.UUID]string) SearchResponse {
	citations := make([]CitationResponse, len(r.Citations))
	for i, c := range r.Citations {
		citations[i] = CitationResponse{
			Number:     c.Number,
			DocumentID: c.DocumentID.String(),
			ChunkID:    c.ChunkID.String(),
			CharStart:  c.CharStart,
			CharEnd:    c.CharEnd,
			Text:       c.Text,
			FileName:   fileNames[c.DocumentID],
			Locator:    citationLocator(c),
		}
	}
	retrievedFiles := make([]RetrievedFileResponse, len(r.RetrievedDocuments))
	for i, docID := range r.RetrievedDocuments {
		retrievedFiles[i] = RetrievedFileResponse{
			DocumentID: docID.String(),
			FileName:   fileNames[docID],
		}
	}

	return SearchResponse{
		Summary:        r.Summary,
		Citations:      citations,
		RetrievedFiles: retrievedFiles,
	}
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
