-- +goose Up
-- Switch every primary key and user_id FK from BIGINT/BIGSERIAL to UUID.
-- gen_random_uuid() requires pgcrypto or Postgres ≥ 13 (built-in).
-- Tables are recreated from scratch; this migration is safe only before
-- any production data exists.

DROP TABLE IF EXISTS chunks;
DROP TABLE IF EXISTS documents;
DROP TABLE IF EXISTS knowledge_bases;
DROP TABLE IF EXISTS users;
DROP TYPE  IF EXISTS document_status;

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE knowledge_bases (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX knowledge_bases_user_id_idx ON knowledge_bases(user_id);

CREATE TYPE document_status AS ENUM ('pending', 'processing', 'indexed', 'failed');

CREATE TABLE documents (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kb_id        UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    filename     TEXT NOT NULL,
    s3_key       TEXT NOT NULL UNIQUE,
    content_type TEXT NOT NULL,
    status       document_status NOT NULL DEFAULT 'pending',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX documents_kb_id_idx   ON documents(kb_id);
CREATE INDEX documents_user_id_idx ON documents(user_id);
CREATE INDEX documents_status_idx  ON documents(status);

CREATE TABLE chunks (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    kb_id       UUID NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ordinal     INT NOT NULL,
    text        TEXT NOT NULL,
    token_count INT NOT NULL DEFAULT 0,
    embedding   vector(1536),
    char_start  INT NOT NULL,
    char_end    INT NOT NULL
);
CREATE INDEX chunks_document_id_idx     ON chunks(document_id);
CREATE INDEX chunks_kb_id_idx           ON chunks(kb_id);
CREATE INDEX chunks_user_id_idx         ON chunks(user_id);
CREATE INDEX chunks_embedding_hnsw_idx  ON chunks USING hnsw (embedding vector_cosine_ops);

-- +goose Down
DROP TABLE IF EXISTS chunks;
DROP TABLE IF EXISTS documents;
DROP TABLE IF EXISTS knowledge_bases;
DROP TABLE IF EXISTS users;
DROP TYPE  IF EXISTS document_status;
