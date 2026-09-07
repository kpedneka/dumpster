// Package pgstore provides a Postgres-backed graphrag.GraphRetriever that
// queries the entity_edges table built during ingestion, seeded via
// canonical_entities so a query resolves to every mention of an identity
// across the KB rather than one chunk-local mention row.
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

// AggregationLeg returns up to k chunks co-occurring with any mention of a
// canonical entity whose normalized text appears in query. Seeds resolve
// through canonical_entities first, then expand to every mention of that
// identity across the KB — not just whichever single chunk-local mention
// happened to match — so results are ordered by total co-occurrence weight
// so the highest-signal chunks rank first.
//
// A minimum normalized-text length of 3 characters guards against trivially
// short strings (single letters, punctuation) matching everything.
//
// A canonical entity not yet linked from a mention (canonicalization is an
// async job stage that can briefly lag entity extraction) is invisible to
// this leg until that job runs; the hybrid vector/keyword legs still cover
// the document in the meantime, and a re-evaluate picks up the graph boost
// once canonicalization catches up.
func (s *Store) AggregationLeg(ctx context.Context, kbID uuid.UUID, query string, k int) ([]retrieval.ScoredChunk, error) {
	userID, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, errors.New("graphrag: aggregation: unauthenticated")
	}

	const q = `
WITH seed_canonicals AS (
    SELECT id
    FROM   canonical_entities
    WHERE  kb_id   = $1
      AND  user_id = $2
      AND  LENGTH(normalized_text) >= 3
      AND  LOWER($3) LIKE '%' || normalized_text || '%'
),
seeds AS (
    SELECT DISTINCT e.id
    FROM   entities e
    JOIN   seed_canonicals sc ON e.canonical_entity_id = sc.id
    WHERE  e.kb_id = $1 AND e.user_id = $2
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
// mentions of a canonical entity whose normalized text appears in query,
// plus any chunk reachable via a confirmed internal/crosslink relationship
// from the seed. It follows seed → hop-1 neighbors → hop-1's *other*
// mentions of the same canonical identity (possibly in other
// chunks/documents entirely) → their co-occurring chunks, excluding chunks
// already returned by the aggregation leg (seed co-occurrence chunks).
//
// The canonical expansion between hop-1 and hop-2 is the fix for this leg's
// previous behavior: entity_edges is chunk-scoped, and entity mentions are
// per-chunk with no dedup, so a specific hop-1 mention row can only ever
// have edges within the one chunk it was extracted in — hopping on the raw
// mention ID therefore only ever reaches chunks already excluded as seed
// chunks, guaranteeing zero rows. Expanding hop-1 to every mention sharing
// its canonical identity is what makes a second, genuinely new hop possible:
// hop-2 can now reach a chunk where some *other* mention of that same
// real-world entity co-occurred with something else entirely.
//
// A hop-1 mention not yet linked to a canonical entity (canonicalization is
// an async job stage that can briefly lag entity extraction) contributes no
// expansion for that specific mention on this call; the hybrid legs still
// cover the document in the meantime, and a re-evaluate picks up the graph
// boost once canonicalization catches up.
//
// cross_linked_chunk_ids is the reason this leg can reach further than two
// hops at all: internal/crosslink pre-computes relationships between
// canonical entities connected by chains longer than this query's own
// two-hop reach can traverse live, storing them as flat
// cross_chunk_edges rows keyed on the canonical identity pair (not any one
// chunk, since the relationship doesn't belong to just one side). Once
// stored, reaching the other side is a single lookup here, not another
// live traversal -- the expensive chain-finding already happened offline.
func (s *Store) TraversalLeg(ctx context.Context, kbID uuid.UUID, query string, k int) ([]retrieval.ScoredChunk, error) {
	userID, ok := auth.UserIDFromContext(ctx)
	if !ok {
		return nil, errors.New("graphrag: traversal: unauthenticated")
	}

	const q = `
WITH seed_canonicals AS (
    SELECT id
    FROM   canonical_entities
    WHERE  kb_id   = $1
      AND  user_id = $2
      AND  LENGTH(normalized_text) >= 3
      AND  LOWER($3) LIKE '%' || normalized_text || '%'
),
seeds AS (
    SELECT DISTINCT e.id
    FROM   entities e
    JOIN   seed_canonicals sc ON e.canonical_entity_id = sc.id
    WHERE  e.kb_id = $1 AND e.user_id = $2
),
seed_chunk_ids AS (
    SELECT DISTINCT ee.chunk_id
    FROM   seeds s
    JOIN   entity_edges ee
             ON ee.entity_a_id = s.id OR ee.entity_b_id = s.id
    WHERE  ee.kb_id   = $1
      AND  ee.user_id = $2
),
hop1_mentions AS (
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
hop1_expanded AS (
    SELECT DISTINCT e2.id
    FROM   hop1_mentions h1
    JOIN   entities e1 ON e1.id = h1.id AND e1.kb_id = $1 AND e1.user_id = $2
    JOIN   entities e2 ON e2.canonical_entity_id = e1.canonical_entity_id
                      AND e2.kb_id = $1 AND e2.user_id = $2
    WHERE  e1.canonical_entity_id IS NOT NULL
),
hop2_chunk_ids AS (
    SELECT DISTINCT ee2.chunk_id
    FROM   hop1_expanded h1e
    JOIN   entity_edges ee2
             ON ee2.entity_a_id = h1e.id OR ee2.entity_b_id = h1e.id
    WHERE  ee2.kb_id   = $1
      AND  ee2.user_id = $2
      AND  ee2.chunk_id NOT IN (SELECT chunk_id FROM seed_chunk_ids)
),
cross_linked AS (
    SELECT DISTINCT
        CASE WHEN cce.canonical_entity_a_id IN (SELECT id FROM seed_canonicals)
             THEN cce.canonical_entity_b_id
             ELSE cce.canonical_entity_a_id
        END AS other_canonical_id
    FROM   cross_chunk_edges cce
    WHERE  cce.kb_id   = $1
      AND  cce.user_id = $2
      AND  cce.relation_type <> 'none'
      AND  (cce.canonical_entity_a_id IN (SELECT id FROM seed_canonicals)
             OR cce.canonical_entity_b_id IN (SELECT id FROM seed_canonicals))
),
cross_linked_chunk_ids AS (
    SELECT DISTINCT em.chunk_id
    FROM   entities em
    JOIN   cross_linked cl ON em.canonical_entity_id = cl.other_canonical_id
    WHERE  em.kb_id = $1 AND em.user_id = $2
      AND  em.chunk_id NOT IN (SELECT chunk_id FROM seed_chunk_ids)
),
reachable_chunk_ids AS (
    SELECT chunk_id FROM hop2_chunk_ids
    UNION
    SELECT chunk_id FROM cross_linked_chunk_ids
)
SELECT DISTINCT c.id, c.document_id, c.kb_id, c.user_id,
                c.ordinal, c.text, c.token_count, c.char_start, c.char_end
FROM   reachable_chunk_ids h2
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
