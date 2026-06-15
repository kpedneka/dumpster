// Package pgstore provides a Postgres-backed retrieval.Retriever that fuses
// pgvector ANN search with Postgres full-text search via Reciprocal Rank Fusion.
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/llm"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
)

// Store is a Postgres-backed retrieval.Retriever.
type Store struct {
	runner   db.TxRunner
	embedder llm.Embedder
}

// New returns a Store wired to the given TxRunner and Embedder.
func New(runner db.TxRunner, embedder llm.Embedder) retrieval.Retriever {
	return &Store{runner: runner, embedder: embedder}
}

// Retrieve embeds query, runs vector and keyword searches inside a single
// transaction, then returns top-k chunks ordered by Reciprocal Rank Fusion score.
func (s *Store) Retrieve(ctx context.Context, kbID uuid.UUID, query string, k int) ([]retrieval.ScoredChunk, error) {
	userID, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, errors.New("retrieval: unauthenticated: no user identity in context")
	}

	vecs, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("retrieval: embed query: %w", err)
	}
	queryVec := vecs[0]

	var vectorRanked, keywordRanked []retrieval.ScoredChunk

	if err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		var err error
		vectorRanked, err = vectorSearch(ctx, tx, kbID, userID, queryVec, k)
		if err != nil {
			return fmt.Errorf("vector search: %w", err)
		}
		keywordRanked, err = keywordSearch(ctx, tx, kbID, userID, query, k)
		if err != nil {
			return fmt.Errorf("keyword search: %w", err)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("retrieval: %w", err)
	}

	return retrieval.RRF(vectorRanked, keywordRanked, k), nil
}

// vectorSearch returns up to k chunks ordered by cosine distance to queryVec.
func vectorSearch(ctx context.Context, tx pgx.Tx, kbID, userID uuid.UUID, queryVec []float32, k int) ([]retrieval.ScoredChunk, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, document_id, kb_id, user_id, ordinal, text, token_count, char_start, char_end
		 FROM chunks
		 WHERE kb_id = $1 AND user_id = $2 AND embedding IS NOT NULL
		 ORDER BY embedding <=> $3::vector
		 LIMIT $4`,
		kbID, userID, vectorLiteral(queryVec), k,
	)
	if err != nil {
		return nil, err
	}
	return scanScoredChunks(rows)
}

// keywordSearch returns up to k chunks matching query via full-text search,
// ordered by ts_rank descending.
func keywordSearch(ctx context.Context, tx pgx.Tx, kbID, userID uuid.UUID, query string, k int) ([]retrieval.ScoredChunk, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, document_id, kb_id, user_id, ordinal, text, token_count, char_start, char_end
		 FROM chunks
		 WHERE kb_id = $1 AND user_id = $2
		   AND text_search @@ websearch_to_tsquery('english', $3)
		 ORDER BY ts_rank(text_search, websearch_to_tsquery('english', $3)) DESC
		 LIMIT $4`,
		kbID, userID, query, k,
	)
	if err != nil {
		return nil, err
	}
	return scanScoredChunks(rows)
}

func scanScoredChunks(rows pgx.Rows) ([]retrieval.ScoredChunk, error) {
	defer rows.Close()
	var out []retrieval.ScoredChunk
	for rows.Next() {
		var c chunk.Chunk
		if err := rows.Scan(
			&c.ID, &c.DocumentID, &c.KBID, &c.UserID,
			&c.Ordinal, &c.Text, &c.TokenCount, &c.CharStart, &c.CharEnd,
		); err != nil {
			return nil, err
		}
		out = append(out, retrieval.ScoredChunk{Chunk: &c})
	}
	return out, rows.Err()
}

// vectorLiteral formats a []float32 as the pgvector text literal "[f1,f2,...]".
func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

var _ retrieval.Retriever = (*Store)(nil)
