// Package crosslink infers relationships between canonical entities that
// live in different chunks and are connected only via a chain longer than
// graphrag.TraversalLeg's hardcoded 2-hop expansion can traverse -- real
// relationships internal/relation structurally cannot see, since its
// candidates are drawn exclusively from same-chunk entity_edges rows that
// already exist. Where internal/relation enriches an edge that already
// exists, this package creates one that doesn't: a genuinely new
// cross-chunk edge, not a re-score of one already there.
//
// Deliberately narrow in scope: this only ever proposes a relationship
// between two entities already resolved to distinct canonical identities.
// It never tries to decide whether two mentions are the same real-world
// entity (that's internal/canonical's job) -- mixing the two would make it
// impossible to tell which fix is responsible for a given improvement.
package crosslink

import (
	"context"

	"github.com/google/uuid"
)

// NoneRelation is the sentinel persisted for a candidate the model reviewed
// but found no clear, specific relationship for -- distinct from never
// having been reviewed at all, so CandidateChains doesn't re-ask about it
// (and re-bill an LLM call) on every subsequent run.
const NoneRelation = "none"

// Candidate is one pair of canonical entities living in different chunks,
// connected only via a chain too long for existing graph traversal to
// reach, not yet checked for an actual relationship. ChunkAText/ChunkBText
// are the source chunks' full text, needed so the model can judge the
// relationship from real context rather than just the two entity names.
type Candidate struct {
	EntityAID   uuid.UUID
	EntityAText string
	ChunkAID    uuid.UUID
	ChunkAText  string

	EntityBID   uuid.UUID
	EntityBText string
	ChunkBID    uuid.UUID
	ChunkBText  string

	// BridgeChunkText is the chunk where the chain's two intermediate
	// entities directly co-occur -- the fact that actually connects
	// EntityA's side to EntityB's side (e.g. "X was renamed Y"). Without
	// it, neither ChunkAText nor ChunkBText alone states any connection at
	// all: the relationship only exists via this intermediate link, so an
	// extractor shown only the two endpoints has no basis to infer
	// anything. Empty if no single chunk directly connects the two
	// intermediates (shouldn't happen for a query built from a real
	// 3-edge path, but defensive rather than assumed).
	BridgeChunkText string
}

// Update is one candidate's extraction outcome, ready to persist.
type Update struct {
	EntityAID, EntityBID uuid.UUID
	RelationType         string
	// ChunkAID/ChunkBID are kept as provenance only (which chunks were
	// actually read to confirm this) -- cross_chunk_edges is keyed on the
	// canonical entity pair, not on either chunk, since the relationship
	// itself doesn't belong to just one of them.
	ChunkAID, ChunkBID uuid.UUID
}

// ConfirmedRelation is one persisted, real (non-NoneRelation) cross-chunk
// relationship, with human-readable entity text -- for review, not for any
// retrieval path. Without this, a run's output is a list of opaque UUIDs
// with no way for a person to judge whether what got confirmed is actually
// correct, or just plausible-sounding noise.
type ConfirmedRelation struct {
	EntityAText, EntityBText, RelationType string
}

// Repository is the persistence boundary: fetching not-yet-checked
// candidate chains and writing back what the model found. Tenant-scoped,
// per the same multi-tenancy contract as every other repository here.
type Repository interface {
	// CandidateChains returns up to limit pairs of canonical entities for
	// kbID that are connected via a chain existing traversal can't reach
	// (no direct entity_edges row between them) and have no prior
	// cross_chunk_edges row (real relation or NoneRelation) recorded yet.
	// Repeated calls make incremental progress across a KB rather than
	// re-asking about pairs a prior run already resolved.
	CandidateChains(ctx context.Context, userID, kbID uuid.UUID, limit int) ([]Candidate, error)

	// ApplyUpdates persists each update as a cross_chunk_edges row.
	ApplyUpdates(ctx context.Context, userID, kbID uuid.UUID, updates []Update) error

	// ListConfirmed returns every real (non-NoneRelation) relationship
	// recorded for kbID, for human review.
	ListConfirmed(ctx context.Context, userID, kbID uuid.UUID) ([]ConfirmedRelation, error)

	// PurgeLowQuality deletes every persisted relation (confirmed or
	// NoneRelation) for kbID involving an entity that wouldn't qualify as a
	// candidate under CandidateChains' current quality rules -- e.g. a
	// generic, non-capitalized "entity" like "region" that canonicalization
	// wrongly merged across two unrelated documents. Exists to clean up
	// relations confirmed before that guard existed; normal operation never
	// calls this. Returns the number of rows removed.
	PurgeLowQuality(ctx context.Context, userID, kbID uuid.UUID) (int, error)
}
