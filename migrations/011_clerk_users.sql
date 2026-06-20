-- +goose Up
-- Replace hand-rolled password-based auth with Clerk-managed identities.
-- Local users rows now key off Clerk's external user ID; password storage
-- is no longer the app's responsibility once session management is
-- delegated to Clerk.
ALTER TABLE users
    ADD COLUMN clerk_user_id TEXT;

-- Backfill is not required: this migration runs before any production
-- data exists for this table (see 007_uuid_user_ids.sql), so a NOT NULL
-- constraint can be applied directly once the column is added.
ALTER TABLE users
    ALTER COLUMN clerk_user_id SET NOT NULL;

ALTER TABLE users
    ADD CONSTRAINT users_clerk_user_id_key UNIQUE (clerk_user_id);

ALTER TABLE users
    DROP COLUMN password_hash;

-- +goose Down
ALTER TABLE users
    ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';

ALTER TABLE users
    ALTER COLUMN password_hash DROP DEFAULT;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_clerk_user_id_key;

ALTER TABLE users
    DROP COLUMN IF EXISTS clerk_user_id;
