package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/search"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

type searchHandler struct {
	kbRepo      kb.Repository
	searcher    search.Searcher
	instruments *telemetry.Instruments // nil when metrics are not configured
}

func registerSearchRoutes(mux *http.ServeMux, kbRepo kb.Repository, searcher search.Searcher, instruments *telemetry.Instruments) {
	h := &searchHandler{kbRepo: kbRepo, searcher: searcher, instruments: instruments}
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

	writeJSON(w, http.StatusOK, toSearchResponse(result))
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
}

// CitationResponse is the JSON shape of a single citation. Number is the
// 1-indexed [N] marker this citation corresponds to in Summary — clients
// must match a marker to a citation by Number, not by array position, since
// this slice is sparse whenever the model didn't cite every numbered chunk.
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
	Text string `json:"text"`
}

func toSearchResponse(r search.Result) SearchResponse {
	citations := make([]CitationResponse, len(r.Citations))
	for i, c := range r.Citations {
		citations[i] = CitationResponse{
			Number:     c.Number,
			DocumentID: c.DocumentID.String(),
			ChunkID:    c.ChunkID.String(),
			CharStart:  c.CharStart,
			CharEnd:    c.CharEnd,
			Text:       c.Text,
		}
	}
	return SearchResponse{
		Summary:   r.Summary,
		Citations: citations,
	}
}
