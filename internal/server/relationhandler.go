package server

import (
	"net/http"

	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/relation"
)

// defaultMaxRelationChunksPerRun bounds how many chunks' worth of candidate
// pairs one POST /kbs/{id}/relations call reviews, keeping the request's
// LLM cost and latency predictable regardless of how many not-yet-checked
// pairs a KB has -- same "0 means use the default" idiom as
// dochandler.go's defaultMaxUploadBytes. Deliberately small: this is a
// synchronous HTTP request, unlike entity/edge extraction's queued worker
// jobs, so it must stay well under any reasonable request timeout.
// Relation extraction is additive and incremental (see
// relation.Repository.CandidateChunks), so reviewing a KB in full is a
// matter of calling this endpoint repeatedly, not raising this constant.
const defaultMaxRelationChunksPerRun = 15

type relationHandler struct {
	kbRepo    kb.Repository
	relations relation.Repository
	extractor *relation.Extractor
	maxChunks int
}

func registerRelationRoutes(mux *http.ServeMux, kbRepo kb.Repository, relations relation.Repository, extractor *relation.Extractor, maxChunks int) {
	if maxChunks <= 0 {
		maxChunks = defaultMaxRelationChunksPerRun
	}
	h := &relationHandler{kbRepo: kbRepo, relations: relations, extractor: extractor, maxChunks: maxChunks}
	mux.HandleFunc("POST /kbs/{id}/relations", h.extract)
}

// extract handles POST /kbs/{id}/relations: reviews up to maxChunks chunks'
// worth of co-occurrence edges that don't have a relation type yet (see
// relation.Repository.CandidateChunks), asks an LLM whether the source
// text actually supports a specific relationship for each pair, and
// persists the result -- a real relation label, or
// relation.NoneRelation when the text doesn't support one. Unlike
// communities/themes, there's nothing to "fail" here for a KB with no
// candidates left: that just means every pair has already been reviewed
// (or none exist yet), which is a normal state, not an error.
func (h *relationHandler) extract(w http.ResponseWriter, r *http.Request) {
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

	chunks, err := h.relations.CandidateChunks(r.Context(), userID, kbID, h.maxChunks)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load candidate chunks")
		return
	}
	if len(chunks) == 0 {
		writeJSON(w, http.StatusOK, relationExtractionResponse{})
		return
	}

	updates, err := h.extractor.Extract(r.Context(), chunks)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to extract relations")
		return
	}
	if err := h.relations.ApplyUpdates(r.Context(), userID, updates); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save extracted relations")
		return
	}

	resp := relationExtractionResponse{ChunksReviewed: len(chunks)}
	for _, c := range chunks {
		resp.PairsReviewed += len(c.Pairs)
	}
	for _, u := range updates {
		if u.RelationType == relation.NoneRelation {
			resp.NoneCount++
		} else {
			resp.RelationsFound++
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// relationExtractionResponse summarizes one POST /kbs/{id}/relations run.
// PairsReviewed can exceed RelationsFound+NoneCount if the model's
// response didn't address every pair (see relation.Extractor.Extract) --
// those pairs are simply left for the next run, not double-counted here.
type relationExtractionResponse struct {
	ChunksReviewed int `json:"chunks_reviewed"`
	PairsReviewed  int `json:"pairs_reviewed"`
	RelationsFound int `json:"relations_found"`
	NoneCount      int `json:"none_count"`
}
