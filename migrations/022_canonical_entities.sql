-- +goose Up
-- Gives cross-chunk, cross-document entity mentions a stable identity.
-- entities rows are per-mention with no dedup, and entity_edges are
-- chunk-scoped, so today there is no way to ask "every chunk that mentions
-- this org, anywhere in the KB" — canonical_entities is that missing
-- identity layer. Matching is exact-normalized-text-plus-type only (see
-- internal/canonical.Normalize); fuzzy/embedding-based matching ("Apple" vs
-- "Apple Inc.") is a deliberate later upgrade, not part of this table.
CREATE TABLE canonical_entities (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kb_id           UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- canonical_text is a display form (the first mention text seen for
    -- this identity); normalized_text is the matching key mentions are
    -- deduped against. The two diverge on casing/whitespace/unicode form
    -- by design — see internal/canonical.Normalize.
    canonical_text  TEXT NOT NULL,
    normalized_text TEXT NOT NULL,
    entity_type     TEXT NOT NULL,
    mention_count   INT NOT NULL DEFAULT 0,
    document_count  INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- This constraint *is* the dedup rule: two mentions collapse to one
-- canonical row iff they agree on KB, tenant, normalized text, and type.
CREATE UNIQUE INDEX canonical_entities_identity_idx
    ON canonical_entities(kb_id, user_id, normalized_text, entity_type);
CREATE INDEX canonical_entities_kb_id_idx   ON canonical_entities(kb_id);
CREATE INDEX canonical_entities_user_id_idx ON canonical_entities(user_id);

ALTER TABLE canonical_entities ENABLE ROW LEVEL SECURITY;
ALTER TABLE canonical_entities FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON canonical_entities
    USING      (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

-- Nullable: a mention starts unresolved and is linked once the async
-- canonicalization job stage (internal/worker.CanonicalizationHandler) has
-- run. ON DELETE SET NULL rather than CASCADE — deleting a canonical row
-- (e.g. its count reaching zero via a decrement) must not take unrelated
-- entity rows down with it.
ALTER TABLE entities ADD COLUMN canonical_entity_id UUID
    REFERENCES canonical_entities(id) ON DELETE SET NULL;
CREATE INDEX entities_canonical_entity_id_idx ON entities(canonical_entity_id);

-- +goose Down
ALTER TABLE entities DROP COLUMN IF EXISTS canonical_entity_id;
DROP POLICY IF EXISTS tenant_isolation ON canonical_entities;
ALTER TABLE canonical_entities DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS canonical_entities;
