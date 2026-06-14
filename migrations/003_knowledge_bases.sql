-- +goose Up
CREATE TABLE knowledge_bases (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX knowledge_bases_user_id_idx ON knowledge_bases(user_id);

-- +goose Down
DROP TABLE IF EXISTS knowledge_bases;
