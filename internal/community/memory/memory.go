// Package memory provides an in-memory community.Repository for use in
// tests.
package memory

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/community"
)

type kbKey struct {
	userID uuid.UUID
	kbID   uuid.UUID
}

// Repository is an in-memory, tenant-scoped community.Repository. Unlike
// the Postgres implementation, it has no entity_edges/canonical_entities
// to derive a graph from — tests seed a KB's graph directly via SeedGraph.
type Repository struct {
	mu      sync.Mutex
	nodes   map[kbKey][]uuid.UUID
	edges   map[kbKey][]community.WeightedEdge
	commIDs map[uuid.UUID]int
	results map[kbKey]community.Result
	// labels/types back GraphView's display fields. Unset for a node means
	// SeedGraph alone was used (no SeedNodeMeta call) -- GraphView falls
	// back to the node's own id string / "entity" rather than an empty
	// label, so existing SeedGraph-only tests don't need updating just
	// because GraphView now exists.
	labels map[uuid.UUID]string
	types  map[uuid.UUID]string
}

// New returns an empty in-memory Repository.
func New() *Repository {
	return &Repository{
		nodes:   make(map[kbKey][]uuid.UUID),
		edges:   make(map[kbKey][]community.WeightedEdge),
		commIDs: make(map[uuid.UUID]int),
		results: make(map[kbKey]community.Result),
		labels:  make(map[uuid.UUID]string),
		types:   make(map[uuid.UUID]string),
	}
}

// SeedNodeMeta sets the display label/type GraphView returns for id.
// Optional -- a node with no metadata seeded falls back to its own id
// string and "entity".
func (r *Repository) SeedNodeMeta(id uuid.UUID, label, entityType string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.labels[id] = label
	r.types[id] = entityType
}

// SeedGraph sets kbID's graph directly, for tests — the in-memory fake has
// no entity_edges/canonical_entities tables to derive one from the way the
// Postgres implementation does.
func (r *Repository) SeedGraph(userID, kbID uuid.UUID, nodes []uuid.UUID, edges []community.WeightedEdge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := kbKey{userID, kbID}
	r.nodes[key] = nodes
	r.edges[key] = edges
}

// CountCanonicalEntities returns the number of nodes seeded for kbID.
func (r *Repository) CountCanonicalEntities(_ context.Context, userID, kbID uuid.UUID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.nodes[kbKey{userID, kbID}]), nil
}

// KBGraph returns the graph seeded for kbID via SeedGraph.
func (r *Repository) KBGraph(_ context.Context, userID, kbID uuid.UUID) (*community.Graph, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := kbKey{userID, kbID}
	return &community.Graph{
		Nodes: append([]uuid.UUID(nil), r.nodes[key]...),
		Edges: append([]community.WeightedEdge(nil), r.edges[key]...),
	}, nil
}

// SaveResult records assignments and run, overwriting kbID's previous
// result.
func (r *Repository) SaveResult(_ context.Context, userID, kbID uuid.UUID, assignments map[uuid.UUID]int, run community.Result) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, cid := range assignments {
		r.commIDs[id] = cid
	}
	run.KBID = kbID
	run.UserID = userID
	r.results[kbKey{userID, kbID}] = run
	return nil
}

// GetResult returns kbID's most recently saved result.
func (r *Repository) GetResult(_ context.Context, userID, kbID uuid.UUID) (*community.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.results[kbKey{userID, kbID}]
	if !ok {
		return nil, community.ErrNoResult
	}
	cp := res
	return &cp, nil
}

// GraphView builds a display-shaped graph from whatever was seeded via
// SeedGraph/SeedNodeMeta and assigned via SaveResult.
func (r *Repository) GraphView(_ context.Context, userID, kbID uuid.UUID) (*community.GraphView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := kbKey{userID, kbID}

	view := &community.GraphView{}
	degree := make(map[uuid.UUID]int)
	for _, e := range r.edges[key] {
		view.Edges = append(view.Edges, community.GraphEdge{Source: e.A, Target: e.B, Weight: e.Weight})
		degree[e.A]++
		degree[e.B]++
	}

	for _, id := range r.nodes[key] {
		n := community.GraphNode{ID: id, Label: r.labels[id], Type: r.types[id], Degree: degree[id]}
		if n.Label == "" {
			n.Label = id.String()
		}
		if n.Type == "" {
			n.Type = "entity"
		}
		if cid, ok := r.commIDs[id]; ok {
			n.CommunityID = &cid
		}
		view.Nodes = append(view.Nodes, n)
	}
	return view, nil
}

// CommunityID returns the community id assigned to canonical entity id by
// the most recent SaveResult call that included it, for test assertions.
func (r *Repository) CommunityID(id uuid.UUID) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cid, ok := r.commIDs[id]
	return cid, ok
}

var _ community.Repository = (*Repository)(nil)
