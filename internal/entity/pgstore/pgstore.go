// Package pgstore provides a Postgres-backed entity.Repository.
package pgstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/entity"
)

// Store is a Postgres-backed implementation of entity.Repository.
type Store struct {
	runner db.TxRunner
}

// New returns a Store backed by runner.
func New(runner db.TxRunner) entity.Repository {
	return &Store{runner: runner}
}

// BulkCreate persists entities in a single transaction.
func (s *Store) BulkCreate(ctx context.Context, entities []*entity.Entity) error {
	if len(entities) == 0 {
		return nil
	}
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		for _, e := range entities {
			_, err := tx.Exec(ctx,
				`INSERT INTO entities
				 (chunk_id, document_id, kb_id, user_id, entity_type, text, char_start, char_end, score)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				e.ChunkID, e.DocumentID, e.KBID, e.UserID,
				string(e.Type), e.Text, e.Start, e.End, e.Score,
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("entity: bulk create: %w", err)
	}
	return nil
}

// ListByDocument returns all entities extracted for documentID, ordered by
// the chunk they belong to and their offset within it.
func (s *Store) ListByDocument(ctx context.Context, userID, documentID uuid.UUID) ([]*entity.Entity, error) {
	var results []*entity.Entity
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT e.id, e.chunk_id, e.document_id, e.kb_id, e.user_id,
			        e.entity_type, e.text, e.char_start, e.char_end, e.score
			 FROM entities e
			 JOIN chunks c ON c.id = e.chunk_id
			 WHERE e.document_id = $1 AND e.user_id = $2
			 ORDER BY c.ordinal, e.char_start`,
			documentID, userID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e entity.Entity
			var entityType string
			if err := rows.Scan(
				&e.ID, &e.ChunkID, &e.DocumentID, &e.KBID, &e.UserID,
				&entityType, &e.Text, &e.Start, &e.End, &e.Score,
			); err != nil {
				return err
			}
			e.Type = entity.Type(entityType)
			results = append(results, &e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("entity: list by document %v: %w", documentID, err)
	}
	return results, nil
}

// DeleteByDocument removes all entities for documentID. Re-running
// extraction calls this first so a re-run is idempotent without touching
// the document's chunks or embeddings.
func (s *Store) DeleteByDocument(ctx context.Context, userID, documentID uuid.UUID) error {
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM entities WHERE document_id = $1 AND user_id = $2`,
			documentID, userID,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("entity: delete by document %v: %w", documentID, err)
	}
	return nil
}

var _ entity.Repository = (*Store)(nil)
