-- +goose Up
-- An Inquiry is a persisted, ordered sequence of query/answer turns a
-- researcher can leave and return to within one knowledge base. v1
-- supports exactly one Inquiry per (kb_id, user_id) — see the unique
-- index below — so callers resolve it via an upsert-style GetOrCreate
-- rather than a plain Create. Relaxing that constraint later (to support
-- multiple inquiries per KB) needs no data migration: just drop the index
-- and add UI for creating additional rows.
CREATE TABLE inquiries (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kb_id      UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    -- Nullable dial room for a future auto-title-suggestion pass to
    -- populate as an addition to the existing row, not a migration.
    title      TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX inquiries_kb_user_key ON inquiries(kb_id, user_id);
CREATE INDEX inquiries_user_id_idx ON inquiries(user_id);

ALTER TABLE inquiries ENABLE ROW LEVEL SECURITY;
ALTER TABLE inquiries FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON inquiries
    USING      (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

-- inquiry_messages holds one row per turn. A user-role row carries only
-- content (the query text); an assistant-role row additionally carries
-- citations/retrieved_documents, mirroring search.Result. user_id is
-- denormalized here (reachable via inquiry_id) so RLS and repository
-- queries stay a direct column filter rather than a join, matching the
-- existing entity_edges-relative-to-chunks precedent.
CREATE TABLE inquiry_messages (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    inquiry_id          UUID NOT NULL REFERENCES inquiries(id) ON DELETE CASCADE,
    kb_id               UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id             UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    role                TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content             TEXT NOT NULL,
    citations           JSONB,
    retrieved_documents JSONB,
    -- ordinal is assigned by the repository as one past the current max
    -- for the inquiry. The unique index below is a safety net against a
    -- concurrent-append race producing a duplicate position, not a
    -- locking mechanism — turns are expected to be appended sequentially
    -- from one browser tab.
    ordinal             INT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX inquiry_messages_inquiry_ordinal_key ON inquiry_messages(inquiry_id, ordinal);
CREATE INDEX inquiry_messages_inquiry_id_idx ON inquiry_messages(inquiry_id);
CREATE INDEX inquiry_messages_user_id_idx ON inquiry_messages(user_id);

ALTER TABLE inquiry_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE inquiry_messages FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON inquiry_messages
    USING      (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON inquiry_messages;
ALTER TABLE inquiry_messages DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS inquiry_messages;

DROP POLICY IF EXISTS tenant_isolation ON inquiries;
ALTER TABLE inquiries DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS inquiries;
