-- +goose Up
CREATE TYPE document_status AS ENUM ('pending', 'processing', 'indexed', 'failed');

CREATE TABLE documents (
    id           BIGSERIAL PRIMARY KEY,
    kb_id        BIGINT NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    filename     TEXT NOT NULL,
    s3_key       TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL,
    status       document_status NOT NULL DEFAULT 'pending',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX documents_kb_id_idx    ON documents(kb_id);
CREATE INDEX documents_user_id_idx  ON documents(user_id);
CREATE INDEX documents_status_idx   ON documents(status);

-- +goose Down
DROP TABLE IF EXISTS documents;
DROP TYPE  IF EXISTS document_status;
