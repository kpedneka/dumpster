-- +goose Up
CREATE TABLE jobs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id  UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    user_id      UUID NOT NULL REFERENCES users(id)     ON DELETE CASCADE,
    status       TEXT NOT NULL DEFAULT 'pending',
    attempts     INT  NOT NULL DEFAULT 0,
    max_attempts INT  NOT NULL DEFAULT 3,
    run_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX jobs_status_run_at_idx ON jobs(status, run_at) WHERE status = 'pending';

-- Ensures at most one active job per document at a time.
CREATE UNIQUE INDEX jobs_document_active_idx ON jobs(document_id)
    WHERE status IN ('pending', 'processing');

-- +goose Down
DROP TABLE IF EXISTS jobs;
