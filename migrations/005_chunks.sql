-- +goose Up
CREATE TABLE chunks (
    id          BIGSERIAL PRIMARY KEY,
    document_id BIGINT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    kb_id       BIGINT NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ordinal     INT NOT NULL,
    text        TEXT NOT NULL,
    token_count INT NOT NULL DEFAULT 0,
    embedding   vector(1536),
    char_start  INT NOT NULL,
    char_end    INT NOT NULL
);

CREATE INDEX chunks_document_id_idx ON chunks(document_id);
CREATE INDEX chunks_kb_id_idx       ON chunks(kb_id);
CREATE INDEX chunks_user_id_idx     ON chunks(user_id);

-- HNSW index for approximate nearest-neighbour search (populated in M5).
CREATE INDEX chunks_embedding_hnsw_idx ON chunks USING hnsw (embedding vector_cosine_ops);

-- +goose Down
DROP TABLE IF EXISTS chunks;
