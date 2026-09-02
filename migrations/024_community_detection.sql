-- +goose Up
-- Adds Louvain-computed community structure to the canonical-entity graph
-- (see internal/community). community_id is nullable — a canonical entity
-- has no community until the first manual "recompute" run for its KB — and
-- is fully overwritten on every subsequent run: community ids are
-- arbitrary per-run integers with no meaning across separate computations,
-- so there is nothing to preserve between runs.
ALTER TABLE canonical_entities ADD COLUMN community_id INT;
CREATE INDEX canonical_entities_community_id_idx ON canonical_entities(kb_id, community_id);

-- One row per KB, upserted on each manual recompute — the run's summary
-- (not the per-entity assignment, which lives on canonical_entities
-- itself). Lets the UI show "communities last computed at X, N communities
-- found" without re-deriving it from a full scan.
CREATE TABLE kb_community_runs (
    kb_id           UUID PRIMARY KEY REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    computed_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    modularity      DOUBLE PRECISION NOT NULL,
    community_count INT NOT NULL,
    node_count      INT NOT NULL,
    edge_count      INT NOT NULL
);
CREATE INDEX kb_community_runs_user_id_idx ON kb_community_runs(user_id);

ALTER TABLE kb_community_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE kb_community_runs FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kb_community_runs
    USING      (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON kb_community_runs;
ALTER TABLE kb_community_runs DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS kb_community_runs;
DROP INDEX IF EXISTS canonical_entities_community_id_idx;
ALTER TABLE canonical_entities DROP COLUMN IF EXISTS community_id;
