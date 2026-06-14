// Package pgstore provides a Postgres-backed chunk.Repository.
// Embedding values are inserted/scanned once pgvector type registration is wired up during chunking & embedding.
package pgstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/db"
)

type Store struct {
	runner db.TxRunner
}

func New(runner db.TxRunner) chunk.Repository {
	return &Store{runner: runner}
}

func (s *Store) BulkCreate(ctx context.Context, chunks []*chunk.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		for _, c := range chunks {
			_, err := tx.Exec(ctx,
				`INSERT INTO chunks
				 (document_id, kb_id, user_id, ordinal, text, token_count, char_start, char_end)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				c.DocumentID, c.KBID, c.UserID, c.Ordinal,
				c.Text, c.TokenCount, c.CharStart, c.CharEnd,
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("chunk: bulk create: %w", err)
	}
	return nil
}

func (s *Store) ListByDocument(ctx context.Context, userID, documentID uuid.UUID) ([]*chunk.Chunk, error) {
	return s.list(ctx, userID, "document_id", documentID)
}

func (s *Store) ListByKB(ctx context.Context, userID, kbID uuid.UUID) ([]*chunk.Chunk, error) {
	return s.list(ctx, userID, "kb_id", kbID)
}

func (s *Store) list(ctx context.Context, userID uuid.UUID, col string, val uuid.UUID) ([]*chunk.Chunk, error) {
	var results []*chunk.Chunk
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		q := fmt.Sprintf(
			`SELECT id, document_id, kb_id, user_id, ordinal, text, token_count, char_start, char_end
			 FROM chunks WHERE %s = $1 AND user_id = $2 ORDER BY ordinal`, col)
		rows, err := tx.Query(ctx, q, val, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c chunk.Chunk
			if err := rows.Scan(
				&c.ID, &c.DocumentID, &c.KBID, &c.UserID,
				&c.Ordinal, &c.Text, &c.TokenCount, &c.CharStart, &c.CharEnd,
			); err != nil {
				return err
			}
			results = append(results, &c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("chunk: list by %s=%v: %w", col, val, err)
	}
	return results, nil
}

func (s *Store) DeleteByDocument(ctx context.Context, userID, documentID uuid.UUID) error {
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM chunks WHERE document_id = $1 AND user_id = $2`,
			documentID, userID,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("chunk: delete by document %v: %w", documentID, err)
	}
	return nil
}

var _ chunk.Repository = (*Store)(nil)
