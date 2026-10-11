-- +goose Up
-- dumpster_app: the role the API and the pipeline Lambdas connect as (the
-- pooled connection string). It neither owns the tables nor has BYPASSRLS,
-- so every tenant_isolation policy actually applies to it; the owner role
-- (neondb_owner on Neon), which bypasses RLS, stays for migrations and the
-- deliberately cross-tenant cleanup task (the direct connection string).
--
-- Created without a password: an operator enables it once per database with
--   ALTER ROLE dumpster_app WITH LOGIN PASSWORD '...';
-- and stores the resulting connection string as the pooled secret, so no
-- credential lives in this repository.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'dumpster_app') THEN
        CREATE ROLE dumpster_app NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
    END IF;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO dumpster_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dumpster_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dumpster_app;
-- The migration bookkeeping table is for the owner only.
REVOKE ALL ON goose_db_version FROM dumpster_app;

-- Tables and sequences later migrations create (run as the owner) are
-- granted automatically, so a new table can't be forgotten.
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO dumpster_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO dumpster_app;

-- +goose Down
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE USAGE, SELECT ON SEQUENCES FROM dumpster_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM dumpster_app;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM dumpster_app;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM dumpster_app;
REVOKE USAGE ON SCHEMA public FROM dumpster_app;
DROP ROLE IF EXISTS dumpster_app;
