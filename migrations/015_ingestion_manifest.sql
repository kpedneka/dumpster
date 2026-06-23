-- +goose Up
-- Ingestion manifest: one row per classified region in a PDF or image
-- document. Records every region the layered classifier (pdfplumber for
-- native text/tables + unstructured.io for layout + Ollama for VLM steps)
-- produces, including regions that were detected but could not be indexed
-- (e.g. scanned text), so "skip" means something beyond silently dropping.
--
-- extractor_version enables future re-index-on-upgrade workflows: compare
-- the stored version against the current extractor to identify regions that
-- can be improved by re-running, without rebuilding from scratch.
CREATE TABLE ingestion_manifest (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id       UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    kb_id             UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- region_type distinguishes native vs. layout-detected vs. VLM-handled
    -- content: native_text | native_table | figure | scanned_text | scanned_table
    region_type       TEXT NOT NULL,
    page_number       INT NOT NULL,
    -- bounding_box stores {x0, y0, x1, y1} as fractional page coordinates
    -- in [0,1] so the coordinates remain valid regardless of render resolution.
    bounding_box      JSONB NOT NULL,
    -- status is indexed (region produced searchable chunks), skipped
    -- (detected but not processable this pass), or failed.
    status            TEXT NOT NULL,
    extractor_version TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ingestion_manifest_document_id_idx ON ingestion_manifest(document_id);
CREATE INDEX ingestion_manifest_user_id_idx     ON ingestion_manifest(user_id);
CREATE INDEX ingestion_manifest_status_idx      ON ingestion_manifest(status);

ALTER TABLE ingestion_manifest ENABLE ROW LEVEL SECURITY;
ALTER TABLE ingestion_manifest FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ingestion_manifest
    USING      (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON ingestion_manifest;
ALTER TABLE ingestion_manifest DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS ingestion_manifest;
