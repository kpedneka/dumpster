-- +goose Up
-- Add a stored generated tsvector column for full-text search.
-- The expression is IMMUTABLE, so Postgres can maintain it automatically
-- on every INSERT/UPDATE without a trigger.
ALTER TABLE chunks
    ADD COLUMN text_search tsvector
        GENERATED ALWAYS AS (to_tsvector('english', text)) STORED;

CREATE INDEX chunks_fts_idx ON chunks USING gin(text_search);

-- +goose Down
DROP INDEX IF EXISTS chunks_fts_idx;
ALTER TABLE chunks DROP COLUMN IF EXISTS text_search;
