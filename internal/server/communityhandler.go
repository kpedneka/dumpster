package server

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"sort"
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
	mux.HandleFunc("GET /kbs/{id}/graph", h.graph)
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
		slog.Error("community: count canonical entities failed", "kb_id", kbID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to check knowledge base size")
		return
	}
	if count > h.maxGraphEntities {
		writeError(w, http.StatusUnprocessableEntity, "knowledge base has too many entities to compute communities")
		return
	}

	graph, err := h.communities.KBGraph(r.Context(), userID, kbID)
	if err != nil {
		slog.Error("community: load kb graph failed", "kb_id", kbID, "err", err)
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
		slog.Error("community: louvain failed", "kb_id", kbID, "err", err)
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
		slog.Error("community: save result failed", "kb_id", kbID, "err", err)
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
		slog.Error("community: get result failed", "kb_id", kbID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load community detection result")
	}
}

// graph handles GET /kbs/{id}/graph: the display-shaped canonical-entity
// graph (nodes with community_id/degree, aggregated edges) the Graph
// Visualization prototype renders, after filterGraphView has trimmed each
// community down to its top few nodes by degree (see that function's own
// comment for why).
func (h *communityHandler) graph(w http.ResponseWriter, r *http.Request) {
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

	view, err := h.communities.GraphView(r.Context(), userID, kbID)
	if err != nil {
		slog.Error("community: load graph view failed", "kb_id", kbID, "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load knowledge base graph")
		return
	}
	view = filterGraphView(view, graphFilterPercentile, graphFilterCap)

	resp := graphViewResponse{
		Nodes: make([]graphNodeResponse, len(view.Nodes)),
		Edges: make([]graphEdgeResponse, len(view.Edges)),
	}
	for i, n := range view.Nodes {
		resp.Nodes[i] = graphNodeResponse{
			ID:            n.ID,
			Label:         n.Label,
			Type:          n.Type,
			CommunityID:   n.CommunityID,
			Degree:        n.Degree,
			DocumentCount: n.DocumentCount,
		}
	}
	for i, e := range view.Edges {
		resp.Edges[i] = graphEdgeResponse{
			Source: e.Source, Target: e.Target, Weight: e.Weight,
			DocumentCount: e.DocumentCount, RelationType: e.RelationType,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// graphFilterPercentile and graphFilterCap bound how much of each
// community's node set /kbs/{id}/graph returns: the top nodes by degree at
// or above this percentile of that community's own degree distribution
// (0.95 = top 5%), or the top graphFilterCap by degree, whichever is
// fewer. Per-community rather than one graph-wide cutoff, so a small
// community isn't wiped out just because a much larger one dominates the
// overall degree distribution.
//
// Motivated by real feedback on the unfiltered first prototype: rendering
// every canonical entity made the browser lag, and most of that volume
// was low-signal noise (stopword-ish extractions like "we", a stray "jan"
// misclassified as an entity) a reader never wants in a community
// overview. Degree is a reasonable proxy for "actually central to this
// community" specifically because noise entities tend not to co-occur
// with many distinct others -- not a guarantee, a heuristic, and one
// worth revisiting if it turns out to cut the wrong nodes in practice.
const (
	graphFilterPercentile = 0.95
	graphFilterCap        = 10
)

// filterGraphView keeps, within each community (and within the
// no-community bucket, treated as its own group), only the top
// min(ceil((1-percentile)*groupSize), cap) nodes by degree, but only for a
// group *larger* than cap -- a group already at or under cap is left fully
// intact, untrimmed. That distinction matters more now than it used to:
// percentile trimming was designed against large, noisy pre-PMI
// communities, but PMI weighting (see community.ApplyPMIWeighting) now
// produces many more, much smaller communities -- averaging under 5
// members in real KBs. The percentile math doesn't know that: for any
// group of 20 members or fewer, ceil(groupSize*0.05) rounds up to exactly
// 1 regardless of groupSize, so a perfectly good, coherent 4-member cluster
// was being reduced to one surviving node -- indistinguishable on screen
// from an actual Louvain singleton, and the direct cause of real observed
// "orphaned dot" reports (e.g. a lone "NSTimes" or "mode" node) even after
// true raw singletons were separately excluded.
//
// After that trim, every edge lacking both endpoints kept is dropped, and
// then -- because a group's survivor(s) can still end up with no surviving
// edge to anything, in or out of its own community, once trimming and edge
// pruning are done -- any node left with zero edges in the *final* graph is
// removed too. This one rule subsumes the "drop raw Louvain singletons"
// special case that used to live here (a raw singleton has zero edges from
// the start, so it was already covered) as well as this newer
// trim-induced case, without needing to special-case either.
func filterGraphView(view *community.GraphView, percentile float64, cap int) *community.GraphView {
	var noCommunity []community.GraphNode
	byCommunity := make(map[int][]community.GraphNode)
	for _, n := range view.Nodes {
		if n.CommunityID == nil {
			noCommunity = append(noCommunity, n)
		} else {
			byCommunity[*n.CommunityID] = append(byCommunity[*n.CommunityID], n)
		}
	}

	kept := make(map[uuid.UUID]bool, len(view.Nodes))
	keepTopByDegree := func(members []community.GraphNode) {
		if len(members) <= cap {
			for _, n := range members {
				kept[n.ID] = true
			}
			return
		}
		sort.Slice(members, func(i, j int) bool { return members[i].Degree > members[j].Degree })
		keepCount := int(math.Ceil(float64(len(members)) * (1 - percentile)))
		if keepCount < 1 {
			keepCount = 1
		}
		if keepCount > cap {
			keepCount = cap
		}
		for _, n := range members[:keepCount] {
			kept[n.ID] = true
		}
	}
	for _, members := range byCommunity {
		keepTopByDegree(members)
	}
	if len(noCommunity) > 0 {
		keepTopByDegree(noCommunity)
	}

	var provisionalEdges []community.GraphEdge
	for _, e := range view.Edges {
		if kept[e.Source] && kept[e.Target] {
			provisionalEdges = append(provisionalEdges, e)
		}
	}

	finalDegree := make(map[uuid.UUID]int, len(kept))
	for _, e := range provisionalEdges {
		finalDegree[e.Source]++
		finalDegree[e.Target]++
	}

	filtered := &community.GraphView{}
	finalKept := make(map[uuid.UUID]bool, len(kept))
	for _, n := range view.Nodes {
		if kept[n.ID] && finalDegree[n.ID] > 0 {
			filtered.Nodes = append(filtered.Nodes, n)
			finalKept[n.ID] = true
		}
	}
	for _, e := range provisionalEdges {
		if finalKept[e.Source] && finalKept[e.Target] {
			filtered.Edges = append(filtered.Edges, e)
		}
	}
	return filtered
}

type graphNodeResponse struct {
	ID            uuid.UUID `json:"id"`
	Label         string    `json:"label"`
	Type          string    `json:"type"`
	CommunityID   *int      `json:"community_id"`
	Degree        int       `json:"degree"`
	DocumentCount int       `json:"document_count"`
}

type graphEdgeResponse struct {
	Source        uuid.UUID `json:"source"`
	Target        uuid.UUID `json:"target"`
	Weight        float64   `json:"weight"`
	DocumentCount int       `json:"document_count"`
	RelationType  *string   `json:"relation_type"`
}

type graphViewResponse struct {
	Nodes []graphNodeResponse `json:"nodes"`
	Edges []graphEdgeResponse `json:"edges"`
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
