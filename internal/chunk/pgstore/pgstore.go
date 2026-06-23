// Package pgstore provides a Postgres-backed chunk.Repository.
package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

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
			var bboxJSON []byte
			if c.BoundingBox != nil {
				var err error
				bboxJSON, err = json.Marshal(c.BoundingBox)
				if err != nil {
					return fmt.Errorf("marshal bounding_box: %w", err)
				}
			}
			_, err := tx.Exec(ctx,
				`INSERT INTO chunks
				 (document_id, kb_id, user_id, ordinal, text, token_count, embedding, char_start, char_end,
				  region_id, page_number, bounding_box)
				 VALUES ($1, $2, $3, $4, $5, $6, $7::vector, $8, $9, $10, $11, $12)`,
				c.DocumentID, c.KBID, c.UserID, c.Ordinal,
				c.Text, c.TokenCount, vectorParam(c.Embedding), c.CharStart, c.CharEnd,
				c.RegionID, c.PageNumber, bboxJSON,
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

// vectorParam formats a []float32 as a pgvector text literal "[f1,f2,...]".
// Returns nil (SQL NULL) when the slice is empty.
func vectorParam(v []float32) any {
	if len(v) == 0 {
		return nil
	}
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
			`SELECT id, document_id, kb_id, user_id, ordinal, text, token_count, char_start, char_end,
			        region_id, page_number, bounding_box
			 FROM chunks WHERE %s = $1 AND user_id = $2 ORDER BY ordinal`, col)
		rows, err := tx.Query(ctx, q, val, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c chunk.Chunk
			var bboxJSON []byte
			if err := rows.Scan(
				&c.ID, &c.DocumentID, &c.KBID, &c.UserID,
				&c.Ordinal, &c.Text, &c.TokenCount, &c.CharStart, &c.CharEnd,
				&c.RegionID, &c.PageNumber, &bboxJSON,
			); err != nil {
				return err
			}
			if len(bboxJSON) > 0 {
				var bb chunk.BoundingBox
				if err := json.Unmarshal(bboxJSON, &bb); err != nil {
					return fmt.Errorf("unmarshal bounding_box: %w", err)
				}
				c.BoundingBox = &bb
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
