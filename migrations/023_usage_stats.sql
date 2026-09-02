-- +goose Up
-- Site-wide, cross-tenant usage counters shown on the public GET /stats
-- endpoint. Deliberately NOT a domain table: no user_id column, no RLS,
-- and never touched by the session sweep / cascade-delete path
-- (internal/account) — the whole point is a number that survives a
-- session's short (2-24h) lifecycle, rather than one derived by scanning
-- tables whose rows disappear when sessions expire. count+total pairs
-- (not a running average) so an average can be computed at read time
-- without floating-point drift accumulating over millions of increments.
--
-- id is a singleton guard: BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id)
-- only ever admits one row (id = TRUE), so every write is an UPDATE
-- against the row seeded below, never an INSERT.
CREATE TABLE usage_stats (
    id                             BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    documents_indexed_count        BIGINT NOT NULL DEFAULT 0,
    documents_indexed_bytes_total  BIGINT NOT NULL DEFAULT 0,
    queries_executed_count         BIGINT NOT NULL DEFAULT 0,
    queries_duration_ms_total      BIGINT NOT NULL DEFAULT 0,
    updated_at                     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO usage_stats (id) VALUES (TRUE);

-- +goose Down
DROP TABLE IF EXISTS usage_stats;
