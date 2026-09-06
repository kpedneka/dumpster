package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/kunalpednekar/dumpster/internal/intrusion"
	"github.com/kunalpednekar/dumpster/internal/kb"
)

type intrusionHandler struct {
	kbRepo  kb.Repository
	members intrusion.MemberSource
	results intrusion.Repository
	tester  *intrusion.Tester
}

// registerIntrusionRoutes wires the intrusion-test endpoints. members is
// intrusion.MemberSource -- the same "every community and its entities"
// query theme.Repository already exposes, so in production this is the
// same theme.Repository instance registerThemeRoutes already wired, not a
// separate store.
func registerIntrusionRoutes(mux *http.ServeMux, kbRepo kb.Repository, members intrusion.MemberSource, results intrusion.Repository, tester *intrusion.Tester) {
	h := &intrusionHandler{kbRepo: kbRepo, members: members, results: results, tester: tester}
	mux.HandleFunc("POST /kbs/{id}/intrusion-test", h.recompute)
	mux.HandleFunc("GET /kbs/{id}/intrusion-test", h.get)
}

// recompute handles POST /kbs/{id}/intrusion-test: runs a fresh intrusion
// test over the KB's current community structure (see intrusion.Tester.Run)
// and persists the result, fully replacing whatever was there before.
// Requires community detection to have already run at least once -- there's
// nothing to test otherwise, same precedent as themehandler's recompute.
func (h *intrusionHandler) recompute(w http.ResponseWriter, r *http.Request) {
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

	members, err := h.members.CommunityMembers(r.Context(), userID, kbID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load community membership")
		return
	}
	if len(members) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "compute communities before running an intrusion test")
		return
	}

	result, err := h.tester.Run(r.Context(), members)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to run intrusion test")
		return
	}
	if result.TestedCount == 0 {
		result.ComputedAt = time.Now()
	}
	if err := h.results.SaveResult(r.Context(), userID, kbID, result); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save intrusion test result")
		return
	}

	writeJSON(w, http.StatusOK, toIntrusionResultResponse(&result))
}

// get handles GET /kbs/{id}/intrusion-test, returning the KB's most
// recently run intrusion test. A KB where one has never been run gets a
// zero-value response (ComputedAt nil), not a 404 -- same first-run
// precedent as GET /kbs/{id}/communities and GET /kbs/{id}/themes.
func (h *intrusionHandler) get(w http.ResponseWriter, r *http.Request) {
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

	result, err := h.results.GetResult(r.Context(), userID, kbID)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, toIntrusionResultResponse(result))
	case errors.Is(err, intrusion.ErrNoResult):
		writeJSON(w, http.StatusOK, intrusionResultResponse{Communities: []intrusionCommunityResponse{}})
	default:
		writeError(w, http.StatusInternalServerError, "failed to load intrusion test result")
	}
}

type intrusionResultResponse struct {
	ComputedAt  *time.Time                   `json:"computed_at"`
	Score       float64                      `json:"score"`
	TestedCount int                          `json:"tested_count"`
	Communities []intrusionCommunityResponse `json:"communities"`
}

type intrusionCommunityResponse struct {
	CommunityID  int      `json:"community_id"`
	Members      []string `json:"members"`
	IntruderText string   `json:"intruder_text"`
	JudgeAnswer  string   `json:"judge_answer"`
	Correct      bool     `json:"correct"`
}

func toIntrusionResultResponse(result *intrusion.Result) intrusionResultResponse {
	resp := intrusionResultResponse{
		Score:       result.Score,
		TestedCount: result.TestedCount,
		Communities: make([]intrusionCommunityResponse, len(result.Communities)),
	}
	if !result.ComputedAt.IsZero() {
		resp.ComputedAt = &result.ComputedAt
	}
	for i, c := range result.Communities {
		resp.Communities[i] = intrusionCommunityResponse{
			CommunityID:  c.CommunityID,
			Members:      c.Members,
			IntruderText: c.IntruderText,
			JudgeAnswer:  c.JudgeAnswer,
			Correct:      c.Correct,
		}
	}
	return resp
}
