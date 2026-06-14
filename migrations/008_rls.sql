-- +goose Up
-- Row-level security backstop on every tenant-scoped table.
-- The app-level WHERE user_id filter remains the primary guard;
-- RLS is a second, independent layer enforced by Postgres itself.
--
-- current_setting('app.current_user_id') (one-arg form) is deliberate:
-- it raises an error when the setting is absent, preventing the silent
-- "zero rows returned" failure that the two-arg form would produce.
--
-- The RLS context is set transaction-locally via set_config(..., true)
-- at the start of each query transaction. Session-level SET is never used
-- because transaction-mode pooling shares backends across requests.

ALTER TABLE knowledge_bases ENABLE  ROW LEVEL SECURITY;
ALTER TABLE knowledge_bases FORCE   ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON knowledge_bases
    USING     (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

ALTER TABLE documents ENABLE  ROW LEVEL SECURITY;
ALTER TABLE documents FORCE   ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON documents
    USING     (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

ALTER TABLE chunks ENABLE  ROW LEVEL SECURITY;
ALTER TABLE chunks FORCE   ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON chunks
    USING     (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON chunks;
ALTER TABLE chunks    DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON documents;
ALTER TABLE documents DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON knowledge_bases;
ALTER TABLE knowledge_bases DISABLE ROW LEVEL SECURITY;
