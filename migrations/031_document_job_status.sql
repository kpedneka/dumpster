-- +goose Up
-- Per-document, per-job-type status for background pipeline work
-- (internal/jobstatus). Replaces the jobs table as what the upload
-- progress UI and the delete/retry guards read, once jobs run on SQS +
-- Lambda and Step Functions instead of the Postgres-polling worker.
--
-- One row per (document, job type), kept after the job finishes: a
-- finished row holds the attempt counter (part of each Step Functions
-- execution name) and, when failed, the error the retry button acts on.
-- Unlike jobs, this is tenant data written from request and job contexts
-- that already carry the user's identity, so it gets the same RLS policy
-- as the other tenant tables.
CREATE TABLE document_job_status (
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    job_type    TEXT NOT NULL,
    user_id     UUID NOT NULL REFERENCES users(id)     ON DELETE CASCADE,
    status      TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'succeeded', 'failed')),
    phase       TEXT,
    attempt     INT  NOT NULL CHECK (attempt >= 1),
    last_error  TEXT,
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (document_id, job_type)
);

-- Serves ActiveJobsForDocuments, which only ever asks for active rows.
CREATE INDEX document_job_status_active_idx ON document_job_status (user_id, document_id)
    WHERE status IN ('pending', 'processing');

ALTER TABLE document_job_status ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_job_status FORCE  ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON document_job_status
    USING      (user_id = current_setting('app.current_user_id')::uuid)
    WITH CHECK (user_id = current_setting('app.current_user_id')::uuid);

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON document_job_status;
ALTER TABLE document_job_status DISABLE ROW LEVEL SECURITY;
DROP TABLE IF EXISTS document_job_status;
