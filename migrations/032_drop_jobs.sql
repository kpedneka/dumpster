-- +goose Up
-- Drops the Postgres job queue. Jobs run on SQS + Lambda and Step Functions
-- now, with per-document status in document_job_status (migration 031), so
-- nothing reads or writes this table. No other table references it.
DROP TABLE IF EXISTS jobs;

-- +goose Down
-- Recreates the table as migrations 009, 012 and 027 left it (users was
-- renamed to sessions in 017). Data isn't restored.
CREATE TABLE jobs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id  UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    user_id      UUID NOT NULL REFERENCES sessions(id)  ON DELETE CASCADE,
    status       TEXT NOT NULL DEFAULT 'pending',
    attempts     INT  NOT NULL DEFAULT 0,
    max_attempts INT  NOT NULL DEFAULT 3,
    run_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    job_type     TEXT NOT NULL DEFAULT 'document_indexing',
    phase        TEXT
);

CREATE INDEX jobs_status_run_at_idx ON jobs(status, run_at) WHERE status = 'pending';

CREATE UNIQUE INDEX jobs_document_type_active_idx ON jobs(document_id, job_type)
    WHERE status IN ('pending', 'processing');
