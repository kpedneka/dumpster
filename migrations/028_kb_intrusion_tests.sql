-- +goose Up
-- Stores word/topic intrusion test results (see internal/intrusion) -- a
-- quantitative coherence metric for a KB's detected communities, letting a
-- pipeline change (chunking, PMI edge weighting, entity filtering, relation
-- extraction) be compared before/after by an actual score rather than by
-- eyeballing a handful of examples. One row per tested community per run;
-- a full run's rows share computed_at. Same per-run-only-meaningful
-- community_id caveat as kb_themes/canonical_entities.community_id: no FK,
-- no meaning across separate community-detection runs.
CREATE TABLE kb_intrusion_tests (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kb_id         UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id       UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    community_id  INT NOT NULL,
    members       TEXT[] NOT NULL,
    intruder_text TEXT NOT NULL,
    judge_answer  TEXT NOT NULL,
    correct       BOOLEAN NOT NULL,
    computed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX kb_intrusion_tests_kb_id_idx   ON kb_intrusion_tests(kb_id);
CREATE INDEX kb_intrusion_tests_user_id_idx ON kb_intrusion_tests(user_id);

ALTER TABLE kb_intrusion_tests ENABLE ROW LEVEL SECURITY;
ALTER TABLE kb_intrusion_tests FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kb_intrusion_tests
    USING      (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON kb_intrusion_tests;
ALTER TABLE kb_intrusion_tests DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS kb_intrusion_tests;
