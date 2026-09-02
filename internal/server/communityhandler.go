package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/community"
	"github.com/kunalpednekar/dumpster/internal/kb"
)

// defaultMaxCommunityGraphEntities and defaultCommunityDetectionTimeout
// apply when the caller passes <= 0, matching the "0 means use the
// default" idiom used elsewhere in this package (see dochandler.go's
// defaultMaxUploadBytes).
const (
	defaultMaxCommunityGraphEntities = 5000
	defaultCommunityDetectionTimeout = 30 * time.Second
)

type communityHandler struct {
	kbRepo           kb.Repository
	communities      community.Repository
	maxGraphEntities int
	timeout          time.Duration
}

func registerCommunityRoutes(mux *http.ServeMux, kbRepo kb.Repository, communities community.Repository, maxGraphEntities int, timeout time.Duration) {
	if maxGraphEntities <= 0 {
		maxGraphEntities = defaultMaxCommunityGraphEntities
	}
	if timeout <= 0 {
		timeout = defaultCommunityDetectionTimeout
	}
	h := &communityHandler{kbRepo: kbRepo, communities: communities, maxGraphEntities: maxGraphEntities, timeout: timeout}
	mux.HandleFunc("POST /kbs/{id}/communities", h.recompute)
	mux.HandleFunc("GET /kbs/{id}/communities", h.get)
}

// recompute handles POST /kbs/{id}/communities: synchronously runs Louvain
// community detection over the KB's canonical-entity graph and persists the
// result, fully replacing whatever was there before. Manual-trigger only —
// not wired into the ingest pipeline or the async job queue (community
// detection is KB-scoped; jobs is hard-scoped to documents).
//
// Two guards protect the API process (256MB) from a pathological KB: a
// cheap COUNT-based pre-check before the graph is ever built, and a context
// deadline enforced during the computation itself (internal/community.
// Louvain polls for cancellation between passes, so this actually stops
// the computation rather than merely abandoning the HTTP response).
func (h *communityHandler) recompute(w http.ResponseWriter, r *http.Request) {
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

	count, err := h.communities.CountCanonicalEntities(r.Context(), userID, kbID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check knowledge base size")
		return
	}
	if count > h.maxGraphEntities {
		writeError(w, http.StatusUnprocessableEntity, "knowledge base has too many entities to compute communities")
		return
	}

	graph, err := h.communities.KBGraph(r.Context(), userID, kbID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load knowledge base graph")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	assignments, modularity, err := community.Louvain(ctx, graph)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusGatewayTimeout, "community detection timed out")
			return
		}
		writeError(w, http.StatusInternalServerError, "community detection failed")
		return
	}

	run := community.Result{
		ComputedAt:     time.Now(),
		Modularity:     modularity,
		CommunityCount: countDistinctCommunities(assignments),
		NodeCount:      len(graph.Nodes),
		EdgeCount:      len(graph.Edges),
	}
	if err := h.communities.SaveResult(r.Context(), userID, kbID, assignments, run); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save community detection result")
		return
	}

	writeJSON(w, http.StatusOK, communityResultResponse{
		ComputedAt:     &run.ComputedAt,
		Modularity:     run.Modularity,
		CommunityCount: run.CommunityCount,
		NodeCount:      run.NodeCount,
		EdgeCount:      run.EdgeCount,
	})
}

// get handles GET /kbs/{id}/communities, returning the KB's most recent
// community-detection summary. A KB where detection has never run gets a
// zero-value response (ComputedAt nil), not a 404 — that's a normal
// first-run state, not an error, matching getInquiry's precedent for a KB
// with no search history yet.
func (h *communityHandler) get(w http.ResponseWriter, r *http.Request) {
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

	result, err := h.communities.GetResult(r.Context(), userID, kbID)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, communityResultResponse{
			ComputedAt:     &result.ComputedAt,
			Modularity:     result.Modularity,
			CommunityCount: result.CommunityCount,
			NodeCount:      result.NodeCount,
			EdgeCount:      result.EdgeCount,
		})
	case errors.Is(err, community.ErrNoResult):
		writeJSON(w, http.StatusOK, communityResultResponse{})
	default:
		writeError(w, http.StatusInternalServerError, "failed to load community detection result")
	}
}

// communityResultResponse is the JSON shape of both community endpoints.
// ComputedAt is nil when detection has never run for the KB.
type communityResultResponse struct {
	ComputedAt     *time.Time `json:"computed_at"`
	Modularity     float64    `json:"modularity"`
	CommunityCount int        `json:"community_count"`
	NodeCount      int        `json:"node_count"`
	EdgeCount      int        `json:"edge_count"`
}

func countDistinctCommunities(assignments map[uuid.UUID]int) int {
	seen := make(map[int]struct{}, len(assignments))
	for _, c := range assignments {
		seen[c] = struct{}{}
	}
	return len(seen)
}
