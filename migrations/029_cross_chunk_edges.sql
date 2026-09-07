-- +goose Up
-- Confirmed relationships between canonical entities that never share a
-- direct entity_edges row and are not reachable within TraversalLeg's
-- hardcoded 2-hop expansion -- real chains that today's graph structurally
-- cannot see. Unlike entity_edges (chunk_id NOT NULL, since a co-occurrence
-- inherently belongs to the one chunk it was observed in), a relationship
-- here spans two chunks by definition, so it's keyed on the canonical
-- identity pair instead -- source_chunk_a_id/source_chunk_b_id are kept
-- only as provenance (which chunks the LLM actually read to confirm this),
-- not as a join key anything else depends on.
CREATE TABLE cross_chunk_edges (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kb_id                 UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id               UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    canonical_entity_a_id UUID NOT NULL REFERENCES canonical_entities(id) ON DELETE CASCADE,
    canonical_entity_b_id UUID NOT NULL REFERENCES canonical_entities(id) ON DELETE CASCADE,
    relation_type         TEXT NOT NULL,
    source_chunk_a_id     UUID NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    source_chunk_b_id     UUID NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT cross_chunk_edges_ordered_pair CHECK (canonical_entity_a_id < canonical_entity_b_id)
);

CREATE UNIQUE INDEX cross_chunk_edges_pair_idx ON cross_chunk_edges(kb_id, user_id, canonical_entity_a_id, canonical_entity_b_id);
CREATE INDEX cross_chunk_edges_entity_a_idx ON cross_chunk_edges(canonical_entity_a_id);
CREATE INDEX cross_chunk_edges_entity_b_idx ON cross_chunk_edges(canonical_entity_b_id);
CREATE INDEX cross_chunk_edges_kb_id_idx    ON cross_chunk_edges(kb_id);
CREATE INDEX cross_chunk_edges_user_id_idx  ON cross_chunk_edges(user_id);

ALTER TABLE cross_chunk_edges ENABLE ROW LEVEL SECURITY;
ALTER TABLE cross_chunk_edges FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON cross_chunk_edges
    USING      (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON cross_chunk_edges;
ALTER TABLE cross_chunk_edges DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS cross_chunk_edges;
