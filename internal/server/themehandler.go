package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/theme"
)

type themeHandler struct {
	kbRepo     kb.Repository
	themes     theme.Repository
	summarizer *theme.Summarizer
}

func registerThemeRoutes(mux *http.ServeMux, kbRepo kb.Repository, themes theme.Repository, summarizer *theme.Summarizer) {
	h := &themeHandler{kbRepo: kbRepo, themes: themes, summarizer: summarizer}
	mux.HandleFunc("POST /kbs/{id}/themes", h.recompute)
	mux.HandleFunc("GET /kbs/{id}/themes", h.get)
}

// recompute handles POST /kbs/{id}/themes: selects the KB's largest
// communities (see theme.SelectTopCommunities) and generates an LLM
// label/summary for each, fully replacing whatever themes were there
// before. Requires community detection to have already run at least once
// for this KB — there's nothing to select communities from otherwise.
func (h *themeHandler) recompute(w http.ResponseWriter, r *http.Request) {
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

	members, err := h.themes.CommunityMembers(r.Context(), userID, kbID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load community membership")
		return
	}
	if len(members) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "compute communities before generating themes")
		return
	}

	top := theme.SelectTopCommunities(members)
	themes, err := h.summarizer.Summarize(r.Context(), top)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate themes")
		return
	}

	computedAt := time.Now()
	if err := h.themes.SaveResult(r.Context(), userID, kbID, themes, computedAt); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save themes")
		return
	}

	writeJSON(w, http.StatusOK, themeResultResponse{
		ComputedAt: &computedAt,
		Themes:     toThemeResponses(themes),
	})
}

// get handles GET /kbs/{id}/themes, returning the KB's most recently
// generated theme set. A KB where themes have never been generated gets a
// zero-value response (ComputedAt nil, empty Themes), not a 404 — same
// first-run precedent as GET /kbs/{id}/communities.
func (h *themeHandler) get(w http.ResponseWriter, r *http.Request) {
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

	result, err := h.themes.GetResult(r.Context(), userID, kbID)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, themeResultResponse{
			ComputedAt: &result.ComputedAt,
			Themes:     toThemeResponses(result.Themes),
		})
	case errors.Is(err, theme.ErrNoResult):
		writeJSON(w, http.StatusOK, themeResultResponse{Themes: []themeResponse{}})
	default:
		writeError(w, http.StatusInternalServerError, "failed to load themes")
	}
}

// themeResultResponse is the JSON shape of both theme endpoints.
// ComputedAt is nil when themes have never been generated for the KB.
type themeResultResponse struct {
	ComputedAt *time.Time      `json:"computed_at"`
	Themes     []themeResponse `json:"themes"`
}

type themeResponse struct {
	CommunityID int    `json:"community_id"`
	Label       string `json:"label"`
	Summary     string `json:"summary"`
	EntityCount int    `json:"entity_count"`
}

func toThemeResponses(themes []theme.Theme) []themeResponse {
	out := make([]themeResponse, len(themes))
	for i, t := range themes {
		out[i] = themeResponse{CommunityID: t.CommunityID, Label: t.Label, Summary: t.Summary, EntityCount: t.EntityCount}
	}
	return out
}
