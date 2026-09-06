// Package relation enriches existing co-occurrence edges (internal/graphedge)
// with an actual relationship type, inferred by an LLM reading the source
// chunk -- the direct fix for edges that otherwise only ever carry a raw
// co-occurrence count, and a genuinely different (semantic, not statistical)
// signal than PMI reweighting (internal/community.ApplyPMIWeighting) can
// ever provide, since PMI can only judge how surprising a pairing's
// frequency is, never what actually connects the two entities.
//
// Deliberately additive, not a replacement: this reads chunks that already
// have a co-occurrence edge (internal/worker's edge-derivation stage, which
// stays exactly as it is) and asks only "does the text support a specific
// relationship between these two, and if so what is it" -- it never
// proposes a pair edge derivation didn't already find. graphedge.Edge's
// RelationType field has existed since migrations/014_entity_edges.sql
// specifically as "dial room" for this; this package is what finally
// populates it.
package relation

import (
	"context"

	"github.com/google/uuid"
)

// EdgePair is one candidate entity pair from a chunk: two entity mentions
// edge derivation already linked by co-occurrence, not yet checked for an
// actual relationship. TextA/TextB are the mentions' surface text, needed
// to name the entities in the extraction prompt.
type EdgePair struct {
	EntityAID uuid.UUID
	TextA     string
	EntityBID uuid.UUID
	TextB     string
}

// ChunkCandidates is one chunk's text plus every not-yet-checked pair
// within it.
type ChunkCandidates struct {
	ChunkID uuid.UUID
	Text    string
	Pairs   []EdgePair
}

// NoneRelation is the sentinel persisted for a pair the model reviewed but
// found no clear, specific relationship for -- distinct from a NULL
// relation_type (never reviewed at all). Without this distinction,
// CandidateChunks would re-select and re-bill an LLM call for the same
// "no relationship here" pairs on every run, forever.
const NoneRelation = "none"

// Update is one pair's extraction outcome, ready to persist.
type Update struct {
	ChunkID              uuid.UUID
	EntityAID, EntityBID uuid.UUID
	RelationType         string
}

// Repository is the persistence boundary: fetching not-yet-checked
// candidates and writing back what the model found. Tenant-scoped, per the
// same multi-tenancy contract as graphedge.Repository.
type Repository interface {
	// CandidateChunks returns up to limit chunks for kbID that have at
	// least one entity_edges row with relation_type still NULL, each with
	// only its not-yet-checked pairs. Repeated calls make incremental
	// progress across a KB rather than re-asking about pairs a prior run
	// already resolved (to a real relation or to NoneRelation) -- limit
	// (and the LLM call it costs) is the only spend control here, and
	// deliberately so.
	//
	// An earlier version of this also required co_occurrence_count >= 2,
	// reasoning that a pair co-occurring only once was the same kind of
	// coincidental noise community.ApplyPMIWeighting's minCoOccurrenceForPMI
	// distrusts. Real data proved that reasoning doesn't transfer: PMI is
	// judging statistical surprise in aggregate co-occurrence counts across
	// a whole KB; this reads the actual source text once, so a relationship
	// stated exactly once ("Gerald Combs founded Wireshark") is often the
	// *cleanest* case, not noise -- and entity_edges.co_occurrence_count is
	// per-chunk besides, so ordinary prose (a pair rarely repeating within
	// one chunk) sits at 1 almost everywhere regardless of how significant
	// the pair is KB-wide. The floor didn't cut noise; it silently emptied
	// the candidate set on exactly the documents (short, single-mention
	// prose) this feature is most useful for. Bounding cost by limit alone,
	// with the caller deciding how many times to call this, is what's left.
	CandidateChunks(ctx context.Context, userID, kbID uuid.UUID, limit int) ([]ChunkCandidates, error)

	// ApplyUpdates sets relation_type on the entity_edges rows matching
	// each update's (chunk_id, entity_a_id, entity_b_id).
	ApplyUpdates(ctx context.Context, userID uuid.UUID, updates []Update) error
}
