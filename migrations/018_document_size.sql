-- +goose Up
ALTER TABLE documents ADD COLUMN size_bytes BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE documents DROP COLUMN size_bytes;
