package community_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/community"
)

// TestLouvain_TwoTrianglesWithWeakBridge is the canonical Louvain
// validation case: two densely-connected triangles joined by one much
// weaker edge should split into two separate communities, one per
// triangle.
func TestLouvain_TwoTrianglesWithWeakBridge(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	d, e, f := uuid.New(), uuid.New(), uuid.New()

	g := &community.Graph{
		Nodes: []uuid.UUID{a, b, c, d, e, f},
		Edges: []community.WeightedEdge{
			{A: a, B: b, Weight: 1.0},
			{A: b, B: c, Weight: 1.0},
			{A: a, B: c, Weight: 1.0},
			{A: d, B: e, Weight: 1.0},
			{A: e, B: f, Weight: 1.0},
			{A: d, B: f, Weight: 1.0},
			{A: c, B: d, Weight: 0.1},
		},
	}

	got, q, err := community.Louvain(context.Background(), g)
	if err != nil {
		t.Fatalf("Louvain: %v", err)
	}
	if q <= 0 {
		t.Errorf("modularity = %v, want > 0 for well-separated clusters", q)
	}

	if got[a] != got[b] || got[b] != got[c] {
		t.Errorf("triangle {a,b,c} split across communities: %v", got)
	}
	if got[d] != got[e] || got[e] != got[f] {
		t.Errorf("triangle {d,e,f} split across communities: %v", got)
	}
	if got[a] == got[d] {
		t.Errorf("the two triangles ended up in the same community despite the weak bridge: %v", got)
	}
}

// TestLouvain_EmptyGraph verifies a graph with no nodes returns an empty
// result rather than erroring.
func TestLouvain_EmptyGraph(t *testing.T) {
	got, q, err := community.Louvain(context.Background(), &community.Graph{})
	if err != nil {
		t.Fatalf("Louvain: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("communities = %v, want empty", got)
	}
	if q != 0 {
		t.Errorf("modularity = %v, want 0", q)
	}
}

// TestLouvain_SingleIsolatedNode verifies one node with no edges gets its
// own community and zero modularity, rather than erroring or dividing by
// zero total edge weight.
func TestLouvain_SingleIsolatedNode(t *testing.T) {
	id := uuid.New()
	g := &community.Graph{Nodes: []uuid.UUID{id}}

	got, q, err := community.Louvain(context.Background(), g)
	if err != nil {
		t.Fatalf("Louvain: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("communities = %v, want exactly 1 entry", got)
	}
	if _, ok := got[id]; !ok {
		t.Errorf("communities = %v, missing the only node", got)
	}
	if q != 0 {
		t.Errorf("modularity = %v, want 0", q)
	}
}

// TestLouvain_AlreadyCancelledContext verifies Louvain checks ctx before
// doing any real work and returns promptly with a wrapped
// context.Canceled, rather than running to completion regardless.
func TestLouvain_AlreadyCancelledContext(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	g := &community.Graph{
		Nodes: []uuid.UUID{a, b, c},
		Edges: []community.WeightedEdge{
			{A: a, B: b, Weight: 1.0},
			{A: b, B: c, Weight: 1.0},
			{A: a, B: c, Weight: 1.0},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, _, err := community.Louvain(ctx, g)
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("Louvain took %v on an already-cancelled context, want well under 100ms", elapsed)
	}
	if err == nil {
		t.Fatal("expected an error for a cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled", err)
	}
}

// TestLouvain_CancellationDuringComputation proves the cancellation check
// fires from inside the algorithm's passes, not only once at function
// entry: a graph large and clustered enough that a full run takes
// meaningfully longer than the deadline should still return promptly once
// that deadline passes, not after running to completion.
func TestLouvain_CancellationDuringComputation(t *testing.T) {
	g := largeClusteredGraph(2000, 40)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := community.Louvain(ctx, g)
	elapsed := time.Since(start)

	if elapsed > 200*time.Millisecond {
		t.Errorf("Louvain took %v after a 1ms timeout, want well under 200ms — cancellation check likely isn't firing mid-computation", elapsed)
	}
	if err == nil {
		t.Fatal("expected an error from the blown deadline, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
}

// largeClusteredGraph builds numClusters dense clusters of clusterSize
// nodes each (a ring of internal edges per cluster, weight 1.0), joined in
// a chain by weak (weight 0.05) bridge edges between consecutive clusters
// — enough structure that Louvain has real, non-trivial work to do across
// many local-moving passes and aggregation levels, rather than converging
// in a single trivial pass.
func largeClusteredGraph(numClusters, clusterSize int) *community.Graph {
	g := &community.Graph{}
	clusters := make([][]uuid.UUID, numClusters)
	for c := 0; c < numClusters; c++ {
		nodes := make([]uuid.UUID, clusterSize)
		for i := range nodes {
			nodes[i] = uuid.New()
		}
		clusters[c] = nodes
		g.Nodes = append(g.Nodes, nodes...)
		for i := 0; i < clusterSize; i++ {
			for j := i + 1; j < clusterSize; j++ {
				g.Edges = append(g.Edges, community.WeightedEdge{A: nodes[i], B: nodes[j], Weight: 1.0})
			}
		}
	}
	for c := 0; c < numClusters-1; c++ {
		g.Edges = append(g.Edges, community.WeightedEdge{
			A: clusters[c][0], B: clusters[c+1][0], Weight: 0.05,
		})
	}
	return g
}
