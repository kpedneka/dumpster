// Package pgstore provides a Postgres-backed graphedge.Repository.
package pgstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/graphedge"
)

// Store is a Postgres-backed implementation of graphedge.Repository.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) graphedge.Repository {
	return &Store{runner: runner}
}

// BulkCreate persists edges in a single transaction. A (chunk_id,
// entity_a_id, entity_b_id) conflict (the same pair re-derived within one
// computation pass) is a no-op rather than an error.
func (s *Store) BulkCreate(ctx context.Context, edges []*graphedge.Edge) error {
	if len(edges) == 0 {
		return nil
	}
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		for _, e := range edges {
			_, err := tx.Exec(ctx,
				`INSERT INTO entity_edges
				 (document_id, kb_id, user_id, chunk_id, entity_a_id, entity_b_id, co_occurrence_count, relation_type)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				 ON CONFLICT (chunk_id, entity_a_id, entity_b_id) DO NOTHING`,
				e.DocumentID, e.KBID, e.UserID, e.ChunkID, e.EntityAID, e.EntityBID,
				e.CoOccurrenceCount, e.RelationType,
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("graphedge: bulk create: %w", err)
	}
	return nil
}

// ListByEntity returns every edge touching entityID, on either side of the
// pair.
func (s *Store) ListByEntity(ctx context.Context, userID, entityID uuid.UUID) ([]*graphedge.Edge, error) {
	var results []*graphedge.Edge
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, document_id, kb_id, user_id, chunk_id, entity_a_id, entity_b_id,
			        co_occurrence_count, relation_type, created_at
			 FROM entity_edges
			 WHERE user_id = $1 AND (entity_a_id = $2 OR entity_b_id = $2)`,
			userID, entityID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e graphedge.Edge
			if err := rows.Scan(
				&e.ID, &e.DocumentID, &e.KBID, &e.UserID, &e.ChunkID, &e.EntityAID, &e.EntityBID,
				&e.CoOccurrenceCount, &e.RelationType, &e.CreatedAt,
			); err != nil {
				return err
			}
			results = append(results, &e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("graphedge: list by entity %v: %w", entityID, err)
	}
	return results, nil
}

// DeleteByDocument removes all edges for documentID. Re-running edge
// extraction calls this first so a re-run is idempotent without touching
// the document's entities or chunks.
func (s *Store) DeleteByDocument(ctx context.Context, userID, documentID uuid.UUID) error {
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM entity_edges WHERE document_id = $1 AND user_id = $2`,
			documentID, userID,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("graphedge: delete by document %v: %w", documentID, err)
	}
	return nil
}

var _ graphedge.Repository = (*Store)(nil)
