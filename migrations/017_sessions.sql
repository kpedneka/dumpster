-- +goose Up
-- Replace the Clerk-backed users table with an anonymous sessions table.
-- The id column (UUID PK) and created_at are preserved as-is; all Clerk-
-- and email-specific columns are dropped; last_active_at is added for
-- tracking activity; warning_sent_at is renamed to warned_at for alignment
-- with the new SessionStore interface.
--
-- PostgreSQL automatically updates foreign-key constraints that reference
-- users(id) to point at sessions(id) when the table is renamed, so the
-- knowledge_bases, documents, and chunks FKs need no explicit migration.

ALTER TABLE users RENAME TO sessions;

-- Drop uniqueness constraints on the columns we are about to remove.
ALTER TABLE sessions DROP CONSTRAINT IF EXISTS users_clerk_user_id_key;
ALTER TABLE sessions DROP CONSTRAINT IF EXISTS users_email_key;

ALTER TABLE sessions RENAME COLUMN warning_sent_at TO warned_at;

ALTER TABLE sessions
    DROP COLUMN IF EXISTS email,
    DROP COLUMN IF EXISTS clerk_user_id,
    ADD COLUMN last_active_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

-- +goose Down
ALTER TABLE sessions DROP COLUMN IF EXISTS last_active_at;
ALTER TABLE sessions RENAME COLUMN warned_at TO warning_sent_at;

ALTER TABLE sessions
    ADD COLUMN email        TEXT NOT NULL DEFAULT '',
    ADD COLUMN clerk_user_id TEXT NOT NULL DEFAULT '';

ALTER TABLE sessions ADD CONSTRAINT users_email_key UNIQUE (email);
ALTER TABLE sessions ADD CONSTRAINT users_clerk_user_id_key UNIQUE (clerk_user_id);

ALTER TABLE sessions RENAME TO users;
