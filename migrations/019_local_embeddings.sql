-- +goose Up
-- Switches the embedding model from OpenAI text-embedding-3-small (1536
-- dims) to the local inference service's BAAI/bge-small-en-v1.5 (384
-- dims, see scripts/embeddings.py). pgvector's vector(N) type is a fixed
-- width, and no automatic reprojection between unrelated embedding spaces
-- exists, so existing vectors must be cleared, not cast, when the
-- dimension changes.
--
-- Chunks left with a nil embedding by this migration simply drop out of
-- vector search until cmd/reembed backfills them with the new model
-- (vectorSearch already filters WHERE embedding IS NOT NULL) — keyword
-- search still covers them via RRF fusion in the meantime. Run cmd/reembed
-- once, promptly after this migration and its accompanying code deploy
-- land.
DROP INDEX IF EXISTS chunks_embedding_hnsw_idx;

ALTER TABLE chunks ALTER COLUMN embedding TYPE vector(384) USING NULL::vector(384);

CREATE INDEX chunks_embedding_hnsw_idx ON chunks USING hnsw (embedding vector_cosine_ops);

-- +goose Down
DROP INDEX IF EXISTS chunks_embedding_hnsw_idx;

ALTER TABLE chunks ALTER COLUMN embedding TYPE vector(1536) USING NULL::vector(1536);

CREATE INDEX chunks_embedding_hnsw_idx ON chunks USING hnsw (embedding vector_cosine_ops);
