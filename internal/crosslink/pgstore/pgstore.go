// Package pgstore provides a Postgres-backed crosslink.Repository.
package pgstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/crosslink"
	"github.com/kunalpednekar/dumpster/internal/db"
)

// Store is a Postgres-backed implementation of crosslink.Repository.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) crosslink.Repository {
	return &Store{runner: runner}
}

// candidateChainsQuery finds canonical-entity pairs connected by exactly
// the kind of chain graphrag.TraversalLeg cannot reach: a path of three
// canonical-entity-level co-occurrence edges through two distinct
// intermediates (entity_a -- mid1 -- mid2 -- entity_b), with no direct
// edge (one hop) and no path of exactly two hops between the endpoints
// either -- both of those are already reachable via existing
// canonicalization + traversal, so proposing them here would just
// re-confirm what the system can already find. Also excludes any pair
// already recorded in cross_chunk_edges (a real relation or
// crosslink.NoneRelation), so repeated calls make incremental progress.
//
// The four distinctness checks in three_hop (beyond entity_a <> entity_b)
// matter more than they look: canon_edges' symmetric closure means a walk
// can legally step backward along an edge it just crossed, so without
// them a walk can "backtrack" (a -> mid1 -> mid2 -> mid1, or similar) and
// get counted as a length-3 path between two nodes that are really one or
// two hops apart -- a real bug caught by two confirmed relations sharing
// an identical chunk pair in an early run, the signature of backtracking
// through the same edge twice.
//
// canon_edges collapses entity_edges (chunk-scoped, per-mention) to the
// canonical-entity level the same way community/pgstore's KBGraph does,
// since a chain has to be reasoned about in terms of stable identities,
// not one chunk's specific mention rows.
//
// The capitalized-text filter on both sides of every edge is a scoped
// precision guard, not a fix to the thing it's guarding against: entity
// extraction's broad location/concept/event/date_time types pull in
// generic vocabulary as "entities" ("region", "market", "storms"), and
// canonicalization's exact-match merging has no sanity check for it --
// two completely unrelated documents that happen to both say "region" get
// treated as mentioning the literal same real-world entity. Confirmed in
// practice: a validation run found "region" bridging two topics built to
// have zero connectivity, because canonicalization merged the word itself,
// not anything either document was actually about. Requiring capitalized
// text excludes ordinary-word false bridges from ever entering the chain
// graph -- including as an intermediate, not just as a candidate endpoint,
// since a bad bridge produces bad candidates on both sides of it. It does
// not fix the underlying canonicalization/extraction issue, which remains
// open (see the Recall Gap Scorecard).
//
// Both checks strip one leading "the"/"a"/"an" (case-insensitive) before
// testing the first letter, not just the raw first character --
// extraction sometimes includes a leading article in the mention span
// ("the Harbor Beacon Leveler"), and checking the raw string would wrongly
// reject a perfectly legitimate proper noun over an included article.
// Confirmed in practice: this exact case broke maritime_2 candidate
// generation entirely after the naive version of this filter shipped.
const candidateChainsQuery = `
WITH canon_edges AS (
    SELECT DISTINCT
        LEAST(e1.canonical_entity_id, e2.canonical_entity_id)    AS a,
        GREATEST(e1.canonical_entity_id, e2.canonical_entity_id) AS b
    FROM   entity_edges ee
    JOIN   entities e1 ON e1.id = ee.entity_a_id
    JOIN   entities e2 ON e2.id = ee.entity_b_id
    JOIN   canonical_entities ca1 ON ca1.id = e1.canonical_entity_id
    JOIN   canonical_entities ca2 ON ca2.id = e2.canonical_entity_id
    WHERE  ee.kb_id = $1 AND ee.user_id = $2
      AND  e1.canonical_entity_id IS NOT NULL
      AND  e2.canonical_entity_id IS NOT NULL
      AND  e1.canonical_entity_id <> e2.canonical_entity_id
      AND  regexp_replace(ca1.canonical_text, '^(the|a|an)\s+', '', 'i') ~ '^[A-Z]'
      AND  regexp_replace(ca2.canonical_text, '^(the|a|an)\s+', '', 'i') ~ '^[A-Z]'
),
sym AS (
    SELECT a, b FROM canon_edges
    UNION
    SELECT b, a FROM canon_edges
),
two_hop AS (
    SELECT DISTINCT s1.a, s2.b
    FROM   sym s1
    JOIN   sym s2 ON s2.a = s1.b
    WHERE  s1.a <> s2.b
),
three_hop AS (
    SELECT DISTINCT s1.a AS entity_a, s1.b AS mid1, s2.b AS mid2, s3.b AS entity_b
    FROM   sym s1
    JOIN   sym s2 ON s2.a = s1.b
    JOIN   sym s3 ON s3.a = s2.b
    WHERE  s1.a <> s3.b   -- entity_a != entity_b
      AND  s1.a <> s2.b   -- entity_a != mid2
      AND  s1.b <> s3.b   -- mid1 != entity_b
),
candidates AS (
    SELECT DISTINCT ON (LEAST(t.entity_a, t.entity_b), GREATEST(t.entity_a, t.entity_b))
           LEAST(t.entity_a, t.entity_b) AS a, GREATEST(t.entity_a, t.entity_b) AS b,
           t.mid1, t.mid2
    FROM   three_hop t
    WHERE  NOT EXISTS (SELECT 1 FROM sym o WHERE o.a = t.entity_a AND o.b = t.entity_b)
      AND  NOT EXISTS (SELECT 1 FROM two_hop w WHERE w.a = t.entity_a AND w.b = t.entity_b)
      AND  NOT EXISTS (
             SELECT 1 FROM cross_chunk_edges cce
             WHERE  cce.kb_id = $1 AND cce.user_id = $2
               AND  cce.canonical_entity_a_id = LEAST(t.entity_a, t.entity_b)
               AND  cce.canonical_entity_b_id = GREATEST(t.entity_a, t.entity_b)
           )
    ORDER  BY LEAST(t.entity_a, t.entity_b), GREATEST(t.entity_a, t.entity_b)
    LIMIT $3
),
bridge_chunk AS (
    SELECT DISTINCT ON (c.a, c.b)
           c.a, c.b, ch.text AS chunk_text
    FROM   candidates c
    JOIN   entity_edges ee ON ee.kb_id = $1 AND ee.user_id = $2
    JOIN   entities     m1 ON m1.id = ee.entity_a_id
    JOIN   entities     m2 ON m2.id = ee.entity_b_id
    JOIN   chunks       ch ON ch.id = ee.chunk_id
    WHERE  (m1.canonical_entity_id = c.mid1 AND m2.canonical_entity_id = c.mid2)
        OR (m1.canonical_entity_id = c.mid2 AND m2.canonical_entity_id = c.mid1)
    ORDER  BY c.a, c.b, ee.id
),
rep_a AS (
    SELECT DISTINCT ON (em.canonical_entity_id)
           em.canonical_entity_id, em.text, em.chunk_id, ch.text AS chunk_text
    FROM   entities em
    JOIN   chunks   ch ON ch.id = em.chunk_id
    WHERE  em.kb_id = $1 AND em.user_id = $2 AND em.canonical_entity_id IN (SELECT a FROM candidates)
    ORDER  BY em.canonical_entity_id, em.id
),
rep_b AS (
    SELECT DISTINCT ON (em.canonical_entity_id)
           em.canonical_entity_id, em.text, em.chunk_id, ch.text AS chunk_text
    FROM   entities em
    JOIN   chunks   ch ON ch.id = em.chunk_id
    WHERE  em.kb_id = $1 AND em.user_id = $2 AND em.canonical_entity_id IN (SELECT b FROM candidates)
    ORDER  BY em.canonical_entity_id, em.id
)
SELECT c.a, ra.text, ra.chunk_id, ra.chunk_text,
       c.b, rb.text, rb.chunk_id, rb.chunk_text,
       COALESCE(bc.chunk_text, '')
FROM   candidates c
JOIN   rep_a ra ON ra.canonical_entity_id = c.a
JOIN   rep_b rb ON rb.canonical_entity_id = c.b
LEFT JOIN bridge_chunk bc ON bc.a = c.a AND bc.b = c.b`

// CandidateChains runs candidateChainsQuery. See its doc comment for what
// qualifies as a candidate.
func (s *Store) CandidateChains(ctx context.Context, userID, kbID uuid.UUID, limit int) ([]crosslink.Candidate, error) {
	var result []crosslink.Candidate
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, candidateChainsQuery, kbID, userID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c crosslink.Candidate
			if err := rows.Scan(
				&c.EntityAID, &c.EntityAText, &c.ChunkAID, &c.ChunkAText,
				&c.EntityBID, &c.EntityBText, &c.ChunkBID, &c.ChunkBText,
				&c.BridgeChunkText,
			); err != nil {
				return err
			}
			result = append(result, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("crosslink: candidate chains: %w", err)
	}
	return result, nil
}

// ApplyUpdates persists each update as a cross_chunk_edges row, upserting
// on the (kb_id, user_id, canonical_entity_a_id, canonical_entity_b_id)
// unique index so a candidate re-reviewed after a prior "none" answer can
// still be corrected by a later, better answer rather than erroring on
// the constraint. Entity order is normalized here (lexicographically
// smaller UUID string first) to satisfy the table's ordered-pair CHECK
// constraint, swapping the provenance chunk ids to match.
func (s *Store) ApplyUpdates(ctx context.Context, userID, kbID uuid.UUID, updates []crosslink.Update) error {
	if len(updates) == 0 {
		return nil
	}
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, u := range updates {
			a, b := u.EntityAID, u.EntityBID
			chunkA, chunkB := u.ChunkAID, u.ChunkBID
			if a.String() > b.String() {
				a, b = b, a
				chunkA, chunkB = chunkB, chunkA
			}
			batch.Queue(
				`INSERT INTO cross_chunk_edges
				   (kb_id, user_id, canonical_entity_a_id, canonical_entity_b_id, relation_type, source_chunk_a_id, source_chunk_b_id)
				 VALUES ($1, $2, $3, $4, $5, $6, $7)
				 ON CONFLICT (kb_id, user_id, canonical_entity_a_id, canonical_entity_b_id)
				 DO UPDATE SET relation_type = EXCLUDED.relation_type`,
				kbID, userID, a, b, u.RelationType, chunkA, chunkB,
			)
		}
		results := tx.SendBatch(ctx, batch)
		for range updates {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return err
			}
		}
		return results.Close()
	})
	if err != nil {
		return fmt.Errorf("crosslink: apply updates: %w", err)
	}
	return nil
}

// ListConfirmed returns every real (non-NoneRelation) relationship
// recorded for kbID, joined to canonical_entities for human-readable text.
func (s *Store) ListConfirmed(ctx context.Context, userID, kbID uuid.UUID) ([]crosslink.ConfirmedRelation, error) {
	var result []crosslink.ConfirmedRelation
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT ca.canonical_text, cb.canonical_text, cce.relation_type
			FROM   cross_chunk_edges cce
			JOIN   canonical_entities ca ON ca.id = cce.canonical_entity_a_id
			JOIN   canonical_entities cb ON cb.id = cce.canonical_entity_b_id
			WHERE  cce.kb_id = $1 AND cce.user_id = $2 AND cce.relation_type <> $3
			ORDER  BY cce.created_at`,
			kbID, userID, crosslink.NoneRelation,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r crosslink.ConfirmedRelation
			if err := rows.Scan(&r.EntityAText, &r.EntityBText, &r.RelationType); err != nil {
				return err
			}
			result = append(result, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("crosslink: list confirmed: %w", err)
	}
	return result, nil
}

// PurgeLowQuality deletes every cross_chunk_edges row for kbID where
// either entity's canonical_text doesn't start with a capital letter, once
// a leading article is stripped -- the same rule candidateChainsQuery
// applies going forward, applied retroactively to relations confirmed
// before that guard (and its leading-article fix) existed.
func (s *Store) PurgeLowQuality(ctx context.Context, userID, kbID uuid.UUID) (int, error) {
	var deleted int
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM cross_chunk_edges cce
			USING  canonical_entities ca, canonical_entities cb
			WHERE  cce.kb_id = $1 AND cce.user_id = $2
			  AND  ca.id = cce.canonical_entity_a_id
			  AND  cb.id = cce.canonical_entity_b_id
			  AND  (regexp_replace(ca.canonical_text, '^(the|a|an)\s+', '', 'i') !~ '^[A-Z]'
			     OR regexp_replace(cb.canonical_text, '^(the|a|an)\s+', '', 'i') !~ '^[A-Z]')`,
			kbID, userID,
		)
		if err != nil {
			return err
		}
		deleted = int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("crosslink: purge low quality: %w", err)
	}
	return deleted, nil
}

var _ crosslink.Repository = (*Store)(nil)
