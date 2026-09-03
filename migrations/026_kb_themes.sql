-- +goose Up
-- Stores LLM-generated theme labels for a KB's largest entity communities
-- (see internal/community for detection itself, migrations/024). One row
-- per selected community per generation run; a full run's rows share
-- computed_at.
CREATE TABLE kb_themes (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kb_id        UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id      UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    -- community_id is only meaningful relative to the community-detection
    -- run active when these themes were generated -- same caveat as
    -- canonical_entities.community_id (migrations/024): no FK, no
    -- cross-run meaning.
    community_id INT NOT NULL,
    label        TEXT NOT NULL,
    summary      TEXT NOT NULL,
    entity_count INT NOT NULL,
    computed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX kb_themes_kb_id_idx   ON kb_themes(kb_id);
CREATE INDEX kb_themes_user_id_idx ON kb_themes(user_id);

ALTER TABLE kb_themes ENABLE ROW LEVEL SECURITY;
ALTER TABLE kb_themes FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kb_themes
    USING      (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON kb_themes;
ALTER TABLE kb_themes DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS kb_themes;
