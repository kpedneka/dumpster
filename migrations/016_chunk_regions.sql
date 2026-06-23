-- +goose Up
-- Adds nullable region provenance columns to chunks so that figure- and
-- table-derived chunks carry the page_number and bounding_box needed for
-- the second citation variant (alongside the existing char_start/char_end
-- variant for native-text chunks). All columns are nullable so that
-- existing text/markdown chunks (which have no manifest region) are
-- unaffected and require no backfill.
ALTER TABLE chunks
    ADD COLUMN region_id    UUID REFERENCES ingestion_manifest(id) ON DELETE SET NULL,
    ADD COLUMN page_number  INT,
    ADD COLUMN bounding_box JSONB;

-- +goose Down
ALTER TABLE chunks
    DROP COLUMN IF EXISTS bounding_box,
    DROP COLUMN IF EXISTS page_number,
    DROP COLUMN IF EXISTS region_id;
