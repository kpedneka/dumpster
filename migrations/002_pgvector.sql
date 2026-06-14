-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;

-- +goose Down
-- Extension is intentionally left; other objects may depend on it.
