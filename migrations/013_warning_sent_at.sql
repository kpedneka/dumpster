-- +goose Up
-- Tracks whether the day-6 demo-account TTL warning email has already been
-- sent, so the daily sweep job is idempotent across repeated runs within
-- the same warning window (day 6 up to day 7) and never double-sends.
-- NULL means "not yet warned"; this also doubles as the field the in-app
-- banner endpoint reads to decide whether to show the warning.
ALTER TABLE users
    ADD COLUMN warning_sent_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE users
    DROP COLUMN IF EXISTS warning_sent_at;
