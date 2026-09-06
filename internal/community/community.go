// Package community computes and stores Louvain community structure over a
// knowledge base's canonical-entity graph (see internal/canonical for what
// a canonical entity is). Detection itself (Graph, WeightedEdge, Louvain)
// is pure graph math with no persistence concerns — see louvain.go; this
// file is the persistence boundary and the per-run summary callers read
// back.
package community

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNoResult is returned by Repository.GetResult when a KB has never had
// community detection run for it.
var ErrNoResult = errors.New("community: no result")

// Result is one KB's most recent community-detection run. Community ids
// are arbitrary per-run integers with no meaning across separate runs, so
// Result carries only the run's summary — the per-entity assignment lives
// on canonical_entities itself.
type Result struct {
	KBID           uuid.UUID
	UserID         uuid.UUID
	ComputedAt     time.Time
	Modularity     float64
	CommunityCount int
	NodeCount      int
	EdgeCount      int
}

// GraphNode is one canonical entity in a KB's graph view, enriched with the
// display and community-membership fields Graph (Louvain's own math-only
// type) doesn't carry.
type GraphNode struct {
	ID    uuid.UUID
	Label string
	Type  string
	// CommunityID is nil if community detection has never been run for
	// this KB, or if it ran before this entity existed.
	CommunityID *int
	// Degree is the number of distinct edges touching this node —
	// computed from the same edge set GraphView returns, not a separate
	// query, so it's always consistent with what's actually rendered.
	Degree int
	// DocumentCount is the number of distinct documents contributing at
	// least one mention to this canonical entity (canonical.CanonicalEntity's
	// own field of the same name, carried through here for display) — a
	// direct signal of cross-document overlap: an entity a KB's several
	// unrelated sources all independently mention is exactly what a reader
	// exploring "how do these documents relate to each other" wants
	// visually distinguished from an entity only one document ever
	// mentions.
	DocumentCount int
}

// GraphEdge is one aggregated co-occurrence edge for display, mirroring
// WeightedEdge's shape under GraphView's own name so callers don't have to
// reach into Louvain's internal math type for a purely presentational
// value.
type GraphEdge struct {
	Source uuid.UUID
	Target uuid.UUID
	Weight float64
	// DocumentCount is the number of distinct documents whose chunks
	// contributed a co-occurrence to this pair — the edge-level equivalent
	// of GraphNode.DocumentCount: two entities a single document happens to
	// mention together read very differently from two entities that
	// multiple independent sources both connect.
	DocumentCount int
}

// GraphView is a KB's canonical-entity graph shaped for display: every
// canonical entity as a node (including isolated ones), its current
// community assignment if one exists, and every aggregated co-occurrence
// edge. This is the type the Graph Visualization endpoint returns — a
// different shape from Graph, which exists only to feed Louvain and knows
// nothing about labels or persisted community assignments.
type GraphView struct {
	Nodes []GraphNode
	Edges []GraphEdge
}

// Repository is the persistence boundary for community detection. Every
// method is tenant-scoped, following the same multi-tenancy contract as
// canonical.Repository.
type Repository interface {
	// CountCanonicalEntities returns the number of canonical entities in
	// kbID — the cheap pre-check callers use to decide whether it's safe to
	// build the full graph and run Louvain, without ever materializing a
	// graph for a KB that's too large.
	CountCanonicalEntities(ctx context.Context, userID, kbID uuid.UUID) (int, error)

	// KBGraph builds the canonical-entity graph for kbID: every canonical
	// entity in the KB as a node (including ones with no edges), and every
	// distinct pair of canonical entities that ever co-occur (via any
	// underlying mention pair in entity_edges) as one weighted edge, summed
	// across every contributing mention pair. Mentions not yet linked to a
	// canonical entity are excluded, matching this codebase's existing
	// pure-canonical precedent (see internal/graphrag/pgstore).
	KBGraph(ctx context.Context, userID, kbID uuid.UUID) (*Graph, error)

	// SaveResult persists assignments (canonical entity id -> community id)
	// onto canonical_entities and records run as kbID's new
	// community-detection summary, replacing whatever was there before —
	// community ids have no meaning across separate runs, so a full
	// overwrite is correct, not a merge.
	SaveResult(ctx context.Context, userID, kbID uuid.UUID, assignments map[uuid.UUID]int, run Result) error

	// GetResult returns kbID's most recent community-detection summary.
	// Returns ErrNoResult if detection has never run for this KB.
	GetResult(ctx context.Context, userID, kbID uuid.UUID) (*Result, error)

	// GraphView returns kbID's canonical-entity graph shaped for display —
	// every canonical entity as a node (with its current community_id, if
	// any) and every aggregated co-occurrence edge. Unlike KBGraph, this is
	// read-only display data, not an input to Louvain, so it carries
	// labels/types and the already-persisted community assignment rather
	// than bare node ids.
	GraphView(ctx context.Context, userID, kbID uuid.UUID) (*GraphView, error)
}
