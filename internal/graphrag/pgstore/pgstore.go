// Package pgstore provides a Postgres-backed graphrag.GraphRetriever that
// queries the entity_edges table built during ingestion.
package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/graphrag"
	"github.com/kunalpednekar/dumpster/internal/retrieval"
)

// Store is a Postgres-backed implementation of graphrag.GraphRetriever.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) graphrag.GraphRetriever {
	return &Store{runner: runner}
}

// AggregationLeg returns up to k chunks co-occurring with any seed entity
// whose text appears in query. Results are ordered by total co-occurrence
// weight so the highest-signal chunks rank first.
//
// A minimum entity text length of 3 characters guards against trivially
// short strings (single letters, punctuation) matching everything.
func (s *Store) AggregationLeg(ctx context.Context, kbID uuid.UUID, query string, k int) ([]retrieval.ScoredChunk, error) {
	userID, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, errors.New("graphrag: aggregation: unauthenticated")
	}

	const q = `
WITH seeds AS (
    SELECT DISTINCT id
    FROM   entities
    WHERE  kb_id   = $1
      AND  user_id = $2
      AND  LENGTH(text) >= 3
      AND  LOWER($3) LIKE '%' || LOWER(text) || '%'
),
edge_chunks AS (
    SELECT   ee.chunk_id,
             SUM(ee.co_occurrence_count) AS weight
    FROM     seeds s
    JOIN     entity_edges ee
               ON ee.entity_a_id = s.id OR ee.entity_b_id = s.id
    WHERE    ee.kb_id   = $1
      AND    ee.user_id = $2
    GROUP BY ee.chunk_id
)
SELECT c.id, c.document_id, c.kb_id, c.user_id,
       c.ordinal, c.text, c.token_count, c.char_start, c.char_end
FROM   edge_chunks ec
JOIN   chunks c ON c.id = ec.chunk_id
ORDER  BY ec.weight DESC
LIMIT  $4`

	var results []retrieval.ScoredChunk
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, kbID, userID, query, k)
		if err != nil {
			return err
		}
		results, err = scanScoredChunks(rows)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("graphrag: aggregation leg: %w", err)
	}
	return results, nil
}

// TraversalLeg returns up to k chunks reachable via two-hop traversal from
// seed entities whose text appears in query. It follows seed → hop-1
// neighbors → their co-occurring chunks, excluding chunks already returned by
// the aggregation leg (seed co-occurrence chunks).
func (s *Store) TraversalLeg(ctx context.Context, kbID uuid.UUID, query string, k int) ([]retrieval.ScoredChunk, error) {
	userID, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, errors.New("graphrag: traversal: unauthenticated")
	}

	const q = `
WITH seeds AS (
    SELECT DISTINCT id
    FROM   entities
    WHERE  kb_id   = $1
      AND  user_id = $2
      AND  LENGTH(text) >= 3
      AND  LOWER($3) LIKE '%' || LOWER(text) || '%'
),
seed_chunk_ids AS (
    SELECT DISTINCT ee.chunk_id
    FROM   seeds s
    JOIN   entity_edges ee
             ON ee.entity_a_id = s.id OR ee.entity_b_id = s.id
    WHERE  ee.kb_id   = $1
      AND  ee.user_id = $2
),
hop1_entity_ids AS (
    SELECT DISTINCT
        CASE WHEN ee.entity_a_id = s.id
             THEN ee.entity_b_id
             ELSE ee.entity_a_id
        END AS id
    FROM   seeds s
    JOIN   entity_edges ee
             ON ee.entity_a_id = s.id OR ee.entity_b_id = s.id
    WHERE  ee.kb_id   = $1
      AND  ee.user_id = $2
),
hop2_chunk_ids AS (
    SELECT DISTINCT ee2.chunk_id
    FROM   hop1_entity_ids h1
    JOIN   entity_edges ee2
             ON ee2.entity_a_id = h1.id OR ee2.entity_b_id = h1.id
    WHERE  ee2.kb_id   = $1
      AND  ee2.user_id = $2
      AND  ee2.chunk_id NOT IN (SELECT chunk_id FROM seed_chunk_ids)
)
SELECT DISTINCT c.id, c.document_id, c.kb_id, c.user_id,
                c.ordinal, c.text, c.token_count, c.char_start, c.char_end
FROM   hop2_chunk_ids h2
JOIN   chunks c ON c.id = h2.chunk_id
LIMIT  $4`

	var results []retrieval.ScoredChunk
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, kbID, userID, query, k)
		if err != nil {
			return err
		}
		results, err = scanScoredChunks(rows)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("graphrag: traversal leg: %w", err)
	}
	return results, nil
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

var _ graphrag.GraphRetriever = (*Store)(nil)
