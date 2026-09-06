// Package pgstore provides a Postgres-backed relation.Repository.
package pgstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/relation"
)

// Store is a Postgres-backed implementation of relation.Repository.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) relation.Repository {
	return &Store{runner: runner}
}

// CandidateChunks returns up to limit chunks for kbID that still have at
// least one entity_edges row with relation_type IS NULL, each carrying
// only its own not-yet-checked pairs. Ordered by chunk id for a stable,
// reproducible selection across runs (matching pairs already resolved --
// to a real relation or to relation.NoneRelation -- fall out of the WHERE
// clause automatically, so later runs naturally pick up where the last
// one left off).
func (s *Store) CandidateChunks(ctx context.Context, userID, kbID uuid.UUID, limit int) ([]relation.ChunkCandidates, error) {
	var result []relation.ChunkCandidates
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT c.id, c.text, ee.entity_a_id, ea.text, ee.entity_b_id, eb.text
			FROM   entity_edges ee
			JOIN   chunks   c  ON c.id = ee.chunk_id
			JOIN   entities ea ON ea.id = ee.entity_a_id
			JOIN   entities eb ON eb.id = ee.entity_b_id
			WHERE  ee.kb_id = $1 AND ee.user_id = $2 AND ee.relation_type IS NULL
			  AND  ee.chunk_id IN (
			          SELECT DISTINCT chunk_id FROM entity_edges
			          WHERE kb_id = $1 AND user_id = $2 AND relation_type IS NULL
			          ORDER BY chunk_id
			          LIMIT $3
			      )
			ORDER BY c.id`,
			kbID, userID, limit,
		)
		if err != nil {
			return err
		}
		defer rows.Close()

		byChunk := make(map[uuid.UUID]*relation.ChunkCandidates)
		var order []uuid.UUID
		for rows.Next() {
			var chunkID, entityAID, entityBID uuid.UUID
			var text, textA, textB string
			if err := rows.Scan(&chunkID, &text, &entityAID, &textA, &entityBID, &textB); err != nil {
				return err
			}
			cc, ok := byChunk[chunkID]
			if !ok {
				cc = &relation.ChunkCandidates{ChunkID: chunkID, Text: text}
				byChunk[chunkID] = cc
				order = append(order, chunkID)
			}
			cc.Pairs = append(cc.Pairs, relation.EdgePair{
				EntityAID: entityAID, TextA: textA,
				EntityBID: entityBID, TextB: textB,
			})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range order {
			result = append(result, *byChunk[id])
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("relation: candidate chunks: %w", err)
	}
	return result, nil
}

// ApplyUpdates sets relation_type on the entity_edges rows matching each
// update's (chunk_id, entity_a_id, entity_b_id). A pair edge derivation
// never created (already deleted, or never existed) simply matches zero
// rows -- not an error, since the candidate set this is applying to was
// read moments earlier and could only have shrunk, never gained an
// inconsistency worth failing over.
func (s *Store) ApplyUpdates(ctx context.Context, userID uuid.UUID, updates []relation.Update) error {
	if len(updates) == 0 {
		return nil
	}
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, u := range updates {
			batch.Queue(
				`UPDATE entity_edges
				 SET    relation_type = $1
				 WHERE  user_id = $2 AND chunk_id = $3 AND entity_a_id = $4 AND entity_b_id = $5`,
				u.RelationType, userID, u.ChunkID, u.EntityAID, u.EntityBID,
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
		return fmt.Errorf("relation: apply updates: %w", err)
	}
	return nil
}

var _ relation.Repository = (*Store)(nil)
