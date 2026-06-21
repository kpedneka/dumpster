-- +goose Up
-- Adds a job_type column so the existing queue/job-stage seam can carry
-- distinct pipeline stages (document indexing, entity extraction, ...),
-- each independently re-runnable without re-touching earlier stages.
ALTER TABLE jobs ADD COLUMN job_type TEXT NOT NULL DEFAULT 'document_indexing';

-- The previous "one active job per document" constraint predates job
-- typing and would incorrectly block an entity-extraction job from being
-- queued while a document-indexing job for the same document is active (or
-- vice versa). Replace it with a constraint scoped per (document, type).
DROP INDEX IF EXISTS jobs_document_active_idx;
CREATE UNIQUE INDEX jobs_document_type_active_idx ON jobs(document_id, job_type)
    WHERE status IN ('pending', 'processing');

-- entities holds local spaCy+GLiNER extraction output: one row per entity
-- mention, linked back to the chunk (and therefore document) it was found
-- in via existing chunk offsets. This is raw material for a future
-- graph-based retrieval feature; it does not touch chunks' embedding or
-- keyword (text_search) columns, so existing hybrid retrieval is unaffected
-- by ingesting, re-running, or deleting entity rows.
CREATE TABLE entities (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chunk_id    UUID NOT NULL REFERENCES chunks(id)    ON DELETE CASCADE,
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    kb_id       UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users(id)     ON DELETE CASCADE,
    -- entity_type is intentionally TEXT, not an ENUM: the allowed type set
    -- is supplied as config (see internal/config ENTITY_TYPES), so adding a
    -- type must not require a migration.
    entity_type TEXT NOT NULL,
    text        TEXT NOT NULL,
    -- char_start/char_end are byte offsets within the chunk's own text,
    -- mirroring chunks.char_start/char_end so a mention can be resolved
    -- back to the source document the same way chunk citations are.
    char_start  INT NOT NULL,
    char_end    INT NOT NULL,
    score       REAL NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX entities_chunk_id_idx     ON entities(chunk_id);
CREATE INDEX entities_document_id_idx  ON entities(document_id);
CREATE INDEX entities_kb_id_idx        ON entities(kb_id);
CREATE INDEX entities_user_id_idx      ON entities(user_id);
CREATE INDEX entities_entity_type_idx  ON entities(entity_type);

ALTER TABLE entities ENABLE  ROW LEVEL SECURITY;
ALTER TABLE entities FORCE   ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON entities
    USING     (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON entities;
ALTER TABLE entities DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS entities;

DROP INDEX IF EXISTS jobs_document_type_active_idx;
CREATE UNIQUE INDEX jobs_document_active_idx ON jobs(document_id)
    WHERE status IN ('pending', 'processing');
ALTER TABLE jobs DROP COLUMN IF EXISTS job_type;
