-- +goose Up
-- Shared-store rate limit counters (internal/ratelimit/pgstore), replacing
-- the in-process internal/ratelimit/memory implementation that silently
-- under-enforces once the API runs more than one replica -- each replica
-- kept its own counter, so N replicas meant the effective limit was N times
-- more permissive.
--
-- No user_id and no RLS, unlike almost every other table in this schema:
-- a rate-limit decision happens before any session or tenant identity
-- exists (it's keyed on raw client IP, applied to every request including
-- unauthenticated ones), so there is no tenant to scope this table by.
-- jobs is the one other table in this schema with the same shape --
-- cross-cutting operational state, not tenant domain data -- and it also
-- has no RLS policy.
CREATE TABLE rate_limit_counters (
    key        TEXT PRIMARY KEY,
    count      INT NOT NULL,
    window_end TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS rate_limit_counters;
