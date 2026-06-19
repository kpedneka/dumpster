package server

import (
	"encoding/json"
	"net/http"

	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/search"
)

type searchHandler struct {
	kbRepo   kb.Repository
	searcher search.Searcher
}

func registerSearchRoutes(mux *http.ServeMux, kbRepo kb.Repository, searcher search.Searcher) {
	h := &searchHandler{kbRepo: kbRepo, searcher: searcher}
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

	result, err := h.searcher.Search(r.Context(), kbID, body.Query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}

	writeJSON(w, http.StatusOK, toSearchResponse(result))
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
		}
	}
	return SearchResponse{
		Summary:   r.Summary,
		Citations: citations,
	}
}
