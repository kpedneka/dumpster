-- +goose Up
-- Untyped co-occurrence edges between entity mentions found in the same
-- chunk — the ingestion-side foundation for GraphRAG aggregation queries
-- ("what orgs are mentioned with FEMA"). entity_a_id/entity_b_id reference
-- individual entity mention rows (v2.1's entities are per-mention, not
-- deduplicated), canonically ordered (entity_a_id < entity_b_id) so a pair
-- found in either extraction order collapses to one row.
CREATE TABLE entity_edges (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id         UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    kb_id               UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    chunk_id            UUID NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
    entity_a_id         UUID NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    entity_b_id         UUID NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    co_occurrence_count INT NOT NULL DEFAULT 1,
    -- Nullable dial room: a later dependency-parse or LLM pass can populate
    -- typed relations as an addition to existing rows, not a migration.
    relation_type       TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT entity_edges_ordered_pair CHECK (entity_a_id < entity_b_id)
);

CREATE UNIQUE INDEX entity_edges_chunk_pair_idx ON entity_edges(chunk_id, entity_a_id, entity_b_id);
CREATE INDEX entity_edges_entity_a_idx    ON entity_edges(entity_a_id);
CREATE INDEX entity_edges_entity_b_idx    ON entity_edges(entity_b_id);
CREATE INDEX entity_edges_document_id_idx ON entity_edges(document_id);
CREATE INDEX entity_edges_user_id_idx     ON entity_edges(user_id);

ALTER TABLE entity_edges ENABLE ROW LEVEL SECURITY;
ALTER TABLE entity_edges FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON entity_edges
    USING      (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON entity_edges;
ALTER TABLE entity_edges DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS entity_edges;
