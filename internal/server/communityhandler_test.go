package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/community"
	communitymem "github.com/kunalpednekar/dumpster/internal/community/memory"
)

// seedTriangleBridgeGraph seeds kbID with two triangles (A,B,C and D,E,F,
// each fully connected internally with weight 1.0) joined by one much
// weaker bridge edge (C-D, weight 0.1) — the standard fixture for
// verifying Louvain finds two separate communities rather than merging
// everything into one.
func seedTriangleBridgeGraph(repo *communitymem.Repository, userID, kbID uuid.UUID) (a, b, c, d, e, f uuid.UUID) {
	a, b, c, d, e, f = uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	nodes := []uuid.UUID{a, b, c, d, e, f}
	edges := []community.WeightedEdge{
		{A: a, B: b, Weight: 1.0},
		{A: b, B: c, Weight: 1.0},
		{A: a, B: c, Weight: 1.0},
		{A: d, B: e, Weight: 1.0},
		{A: e, B: f, Weight: 1.0},
		{A: d, B: f, Weight: 1.0},
		{A: c, B: d, Weight: 0.1},
	}
	repo.SeedGraph(userID, kbID, nodes, edges)
	return
}

func TestCommunityRecompute_ComputesAndPersists(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	repo := deps.Communities.(*communitymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	seedTriangleBridgeGraph(repo, userID, k.ID)
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/communities", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got communityResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.NodeCount != 6 {
		t.Errorf("NodeCount = %d, want 6", got.NodeCount)
	}
	if got.EdgeCount != 7 {
		t.Errorf("EdgeCount = %d, want 7", got.EdgeCount)
	}
	if got.CommunityCount != 2 {
		t.Errorf("CommunityCount = %d, want 2 (two triangles joined by a weak bridge)", got.CommunityCount)
	}
	if got.ComputedAt == nil {
		t.Error("ComputedAt should be set after a successful recompute")
	}
}

// TestGraphView_ReturnsFilteredNodesWithCommunityIDAndDegree exercises the
// full /kbs/{id}/graph path including its per-community top-by-degree
// filter (see filterGraphView). The triangle-bridge fixture happens to
// give the bridge's two endpoints (c, d) a strictly higher degree (3) than
// their triangle-mates (2) -- no ties to worry about -- so with each
// 3-member community's keep-count at min(ceil(3*0.05), 10) = 1, c and d
// are the deterministic survivors.
func TestGraphView_ReturnsFilteredNodesWithCommunityIDAndDegree(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	repo := deps.Communities.(*communitymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	_, _, c, d, _, _ := seedTriangleBridgeGraph(repo, userID, k.ID)
	repo.SeedNodeMeta(c, "Ada Lovelace", "person")
	router := NewRouter(deps)

	// Compute communities first so community_id is actually populated --
	// GraphView reads whatever the most recent recompute assigned, it
	// doesn't compute anything itself.
	recomputeReq := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/communities", nil, userID)
	router.ServeHTTP(httptest.NewRecorder(), recomputeReq)

	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/graph", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got graphViewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Both communities here have 3 members -- at or under graphFilterCap --
	// so filterGraphView's percentile trim doesn't apply to either of them
	// at all (see its own doc comment for why trimming a community already
	// at or under the cap was actively harmful). Every node and edge from
	// this fixture is expected to survive untouched.
	if len(got.Nodes) != 6 {
		t.Fatalf("len(Nodes) = %d, want 6 (both 3-member communities are under the cap, so nothing is trimmed)", len(got.Nodes))
	}
	if len(got.Edges) != 7 {
		t.Fatalf("len(Edges) = %d, want 7 (all edges survive -- nothing was trimmed)", len(got.Edges))
	}

	byID := make(map[uuid.UUID]graphNodeResponse, len(got.Nodes))
	for _, n := range got.Nodes {
		byID[n.ID] = n
	}
	nodeC, ok := byID[c]
	if !ok {
		t.Fatal("node c (highest degree in its triangle) should have survived filtering")
	}
	if nodeC.Label != "Ada Lovelace" || nodeC.Type != "person" {
		t.Errorf("node c = %+v, want seeded label/type carried through", nodeC)
	}
	if nodeC.CommunityID == nil {
		t.Error("node c's CommunityID should be set after a recompute")
	}
	// Degree reflects the node's true degree in the full graph, computed
	// before filtering -- not how many of its edges happened to survive
	// (which would be circular, since filtering is itself degree-based).
	if nodeC.Degree != 3 {
		t.Errorf("node c's Degree = %d, want 3 (edges to a, b, and the d bridge)", nodeC.Degree)
	}

	nodeD, ok := byID[d]
	if !ok {
		t.Fatal("node d (highest degree in its triangle) should have survived filtering")
	}
	if nodeD.CommunityID == nil {
		t.Fatal("node d should have a community assigned")
	}
	if *nodeC.CommunityID == *nodeD.CommunityID {
		t.Errorf("nodes c and d landed in the same community (%d) -- expected the bridge to separate them", *nodeC.CommunityID)
	}
}

// TestFilterGraphView_CapsAtGraphFilterCapForLargeCommunities exercises the
// cap side of "95th percentile or 10, whichever is smaller" -- a fixture
// large enough (200 members) that 5% of it (10) exactly equals the cap,
// then one member larger (201) to confirm the percentile side would want
// 11 but the cap still holds it at 10.
func TestFilterGraphView_CapsAtGraphFilterCapForLargeCommunities(t *testing.T) {
	makeCommunityOfSize := func(n int, communityID int) []community.GraphNode {
		nodes := make([]community.GraphNode, n)
		for i := range nodes {
			cid := communityID
			// Strictly decreasing degree, so "top 10 by degree" has an
			// unambiguous, order-independent answer.
			nodes[i] = community.GraphNode{ID: uuid.New(), CommunityID: &cid, Degree: n - i}
		}
		return nodes
	}

	nodes := makeCommunityOfSize(201, 1)
	// Chain the top graphFilterCap nodes (the ones expected to survive)
	// together so none of them end up with zero surviving edges -- without
	// this, filterGraphView's final zero-degree pass would drop every one
	// of them, since this fixture otherwise has no edges at all.
	var edges []community.GraphEdge
	for i := 0; i < graphFilterCap-1; i++ {
		edges = append(edges, community.GraphEdge{Source: nodes[i].ID, Target: nodes[i+1].ID, Weight: 1})
	}
	view := &community.GraphView{Nodes: nodes, Edges: edges}
	got := filterGraphView(view, graphFilterPercentile, graphFilterCap)

	if len(got.Nodes) != graphFilterCap {
		t.Fatalf("len(Nodes) = %d, want %d (cap, even though 5%% of 201 rounds up to 11)", len(got.Nodes), graphFilterCap)
	}
	for i, n := range got.Nodes {
		if n.Degree != 201-i {
			t.Errorf("kept node %d has Degree %d, want the top-%d highest degrees in order", i, n.Degree, graphFilterCap)
		}
	}
}

// TestFilterGraphView_PrunesEdgesWithADroppedEndpoint confirms the
// returned graph stays internally consistent -- an edge with one endpoint
// filtered out never appears in the response, even though the surviving
// endpoint does. Community 1 has 11 members (just over graphFilterCap) so
// the percentile/cap trim actually applies -- a community at or under the
// cap is left fully intact and would trivially pass this test without
// exercising the edge-pruning logic at all.
func TestFilterGraphView_PrunesEdgesWithADroppedEndpoint(t *testing.T) {
	cid, bridgeCid := 1, 2
	kept := uuid.New()
	dropped1 := uuid.New()
	bridgeTarget := uuid.New()

	// 9 filler members give community 1 eleven members total (kept,
	// dropped1, and these) -- enough to exceed graphFilterCap and trigger
	// real trimming down to just the single highest-degree survivor.
	nodes := []community.GraphNode{
		{ID: kept, CommunityID: &cid, Degree: 20},
		{ID: dropped1, CommunityID: &cid, Degree: 10},
	}
	for i := 0; i < 9; i++ {
		nodes = append(nodes, community.GraphNode{ID: uuid.New(), CommunityID: &cid, Degree: 5})
	}
	// bridgeTarget's own community is small enough (2 members) to survive
	// the trim fully intact, giving "kept" a real cross-community edge to
	// end up with -- otherwise, once dropped1 is trimmed away, "kept" would
	// have zero surviving edges and be removed by the final zero-degree
	// pass this same function applies, which isn't what this test is about.
	bridgeMate := uuid.New()
	nodes = append(nodes,
		community.GraphNode{ID: bridgeTarget, CommunityID: &bridgeCid, Degree: 2},
		community.GraphNode{ID: bridgeMate, CommunityID: &bridgeCid, Degree: 1},
	)

	view := &community.GraphView{
		Nodes: nodes,
		Edges: []community.GraphEdge{
			{Source: kept, Target: dropped1, Weight: 1},
			{Source: kept, Target: bridgeTarget, Weight: 1},
			// Without this, bridgeMate would have zero surviving edges of
			// its own and be dropped by the final zero-degree pass, even
			// though its community (bridgeCid) is under the cap and
			// therefore not supposed to be trimmed at all.
			{Source: bridgeTarget, Target: bridgeMate, Weight: 1},
		},
	}

	got := filterGraphView(view, graphFilterPercentile, graphFilterCap)

	var gotIDs []uuid.UUID
	for _, n := range got.Nodes {
		gotIDs = append(gotIDs, n.ID)
	}
	if len(got.Nodes) != 3 {
		t.Fatalf("Nodes = %+v, want 3 (kept, bridgeTarget, and bridgeTarget's community-mate)", gotIDs)
	}
	if len(got.Edges) != 2 {
		t.Errorf("Edges = %+v, want 2 (kept-bridgeTarget and bridgeTarget-bridgeMate) -- kept-dropped1 is pruned since dropped1 didn't survive the trim", got.Edges)
	}
}

// TestFilterGraphView_DropsSingletonCommunities confirms a community with
// exactly one member -- Louvain's own signature for "this entity had no
// surviving edge to anything else" -- is dropped entirely, not kept as a
// lone node with nothing to relate it to the rest of the graph.
func TestFilterGraphView_DropsSingletonCommunities(t *testing.T) {
	singletonCid, pairCid := 1, 2
	isolated := uuid.New()
	a, b := uuid.New(), uuid.New()
	view := &community.GraphView{
		Nodes: []community.GraphNode{
			{ID: isolated, CommunityID: &singletonCid, Degree: 0},
			// A real 2-member community is under graphFilterCap, so it's
			// left fully intact (see filterGraphView's doc comment) -- both
			// members survive with their edge to each other. This test is
			// about the singleton being dropped outright, in contrast.
			{ID: a, CommunityID: &pairCid, Degree: 2},
			{ID: b, CommunityID: &pairCid, Degree: 1},
		},
		Edges: []community.GraphEdge{
			{Source: a, Target: b, Weight: 1},
		},
	}

	got := filterGraphView(view, graphFilterPercentile, graphFilterCap)

	for _, n := range got.Nodes {
		if n.ID == isolated {
			t.Fatalf("Nodes = %+v, want the singleton-community node dropped", got.Nodes)
		}
	}
	if len(got.Nodes) != 2 {
		t.Errorf("Nodes = %+v, want both members of the real 2-member community kept intact", got.Nodes)
	}
	if len(got.Edges) != 1 {
		t.Errorf("Edges = %+v, want the a-b edge preserved", got.Edges)
	}
}

// TestFilterGraphView_NoCommunityNodesAreTheirOwnGroup confirms entities
// with no community assignment (CommunityID nil) are filtered as their
// own group, not lumped in with an actual community, and is subject to the
// same over-the-cap trimming a real community would get. The no-community
// bucket needs more than graphFilterCap members here for that trimming to
// actually apply -- see filterGraphView's own doc comment on why a group at
// or under the cap is left fully intact instead.
func TestFilterGraphView_NoCommunityNodesAreTheirOwnGroup(t *testing.T) {
	high := uuid.New()
	nodes := []community.GraphNode{{ID: high, CommunityID: nil, Degree: 20}}
	for i := 0; i < 10; i++ {
		nodes = append(nodes, community.GraphNode{ID: uuid.New(), CommunityID: nil, Degree: 5})
	}
	// A partner in a real (small, under-cap) community, so "high" has a
	// surviving edge to end up with instead of being dropped by the final
	// zero-degree pass -- this test is about the no-community group's own
	// trimming, not that unrelated mechanism.
	cid := 1
	partner := uuid.New()
	nodes = append(nodes, community.GraphNode{ID: partner, CommunityID: &cid, Degree: 1})

	view := &community.GraphView{
		Nodes: nodes,
		Edges: []community.GraphEdge{{Source: high, Target: partner, Weight: 1}},
	}

	got := filterGraphView(view, graphFilterPercentile, graphFilterCap)

	var survivedFromNoCommunity int
	var sawHigh bool
	for _, n := range got.Nodes {
		if n.CommunityID == nil {
			survivedFromNoCommunity++
			if n.ID == high {
				sawHigh = true
			}
		}
	}
	if survivedFromNoCommunity != 1 || !sawHigh {
		t.Fatalf("Nodes = %+v, want only the higher-degree no-community node kept", got.Nodes)
	}
}

func TestGraphView_KBNotFound_Returns404(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	userID := uuid.New()
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+uuid.New().String()+"/graph", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestCommunityRecompute_TooManyEntities_Returns422(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	deps.MaxCommunityGraphEntities = 2
	repo := deps.Communities.(*communitymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	seedTriangleBridgeGraph(repo, userID, k.ID) // 6 nodes, over the cap of 2
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/communities", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: got %d, want 422, body: %s", w.Code, w.Body.String())
	}

	// The graph must never have been built/computed over — GetResult should
	// still report no result, proving the pre-check short-circuited before
	// any computation was attempted.
	if _, err := repo.GetResult(context.TODO(), userID, k.ID); err != community.ErrNoResult {
		t.Errorf("GetResult after a rejected recompute: got err %v, want ErrNoResult", err)
	}
}

func TestCommunityGet_NoResultYet_ReturnsZeroValue(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	router := NewRouter(deps)

	req := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/communities", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got communityResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ComputedAt != nil {
		t.Errorf("ComputedAt = %v, want nil before any recompute has run", got.ComputedAt)
	}
}

func TestCommunityGet_AfterRecompute_ReturnsSummary(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	repo := deps.Communities.(*communitymem.Repository)
	userID := uuid.New()
	k, _ := kbRepo.Create(context.TODO(), userID, "kb1")
	seedTriangleBridgeGraph(repo, userID, k.ID)
	router := NewRouter(deps)

	postReq := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/communities", nil, userID)
	router.ServeHTTP(httptest.NewRecorder(), postReq)

	getReq := authedRequest(t, deps, http.MethodGet, "/kbs/"+k.ID.String()+"/communities", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, getReq)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var got communityResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ComputedAt == nil || got.CommunityCount != 2 {
		t.Errorf("GET after recompute = %+v, want ComputedAt set and CommunityCount 2", got)
	}
}

func TestCommunity_KBNotFound_Returns404(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	userID := uuid.New()

	postReq := authedRequest(t, deps, http.MethodPost, "/kbs/"+uuid.New().String()+"/communities", nil, userID)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, postReq)
	if w.Code != http.StatusNotFound {
		t.Errorf("POST unknown KB: got %d, want 404", w.Code)
	}

	getReq := authedRequest(t, deps, http.MethodGet, "/kbs/"+uuid.New().String()+"/communities", nil, userID)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, getReq)
	if w2.Code != http.StatusNotFound {
		t.Errorf("GET unknown KB: got %d, want 404", w2.Code)
	}
}

func TestCommunity_TenantIsolation(t *testing.T) {
	deps, kbRepo, _, _, _ := defaultDeps()
	router := NewRouter(deps)
	owner, other := uuid.New(), uuid.New()
	k, _ := kbRepo.Create(context.TODO(), owner, "kb1")

	req := authedRequest(t, deps, http.MethodPost, "/kbs/"+k.ID.String()+"/communities", nil, other)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant recompute: got %d, want 404", w.Code)
	}
}
