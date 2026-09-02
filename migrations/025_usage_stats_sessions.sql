-- +goose Up
-- Extends the site-wide usage_stats singleton (migrations/023) with two more
-- durable counters: total sessions ever created and total sessions ever
-- swept (hard-deleted for idle-timeout/hard-cap expiry — see
-- internal/account.Sweep). Same rationale as the rest of usage_stats:
-- sessions themselves are short-lived (2-24h) and get deleted on sweep, so
-- these can only be answered by a durable counter incremented at the
-- moment each event happens, not a scan of the sessions table.
ALTER TABLE usage_stats ADD COLUMN sessions_created_count INT NOT NULL DEFAULT 0;
ALTER TABLE usage_stats ADD COLUMN sessions_swept_count   INT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE usage_stats DROP COLUMN IF EXISTS sessions_swept_count;
ALTER TABLE usage_stats DROP COLUMN IF EXISTS sessions_created_count;
