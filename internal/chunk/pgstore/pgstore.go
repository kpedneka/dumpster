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

// chunkBulkCreateColumns lists the columns BulkCreate's multi-row VALUES
// INSERT writes, in the exact order each row's placeholders/args below are
// built in. id, created_at, and updated_at are deliberately omitted --
// all have DB-side defaults this never overrode even in the single-row
// Exec version.
const chunkBulkCreateColumns = `document_id, kb_id, user_id, ordinal, text, token_count, embedding, char_start, char_end, region_id, page_number, bounding_box`

// chunkBulkCreateColumnCount must match the number of columns in
// chunkBulkCreateColumns and placeholders/args built per row below --
// used to keep each INSERT statement under Postgres's 65535-parameter
// hard limit as rowsPerStatement.
const chunkBulkCreateColumnCount = 12

// chunkBulkCreateRowsPerStatement caps how many chunks go into a single
// multi-row INSERT statement. 65535 params / 12 per row = 5461 rows is
// the hard ceiling; this leaves real margin below it while still cutting
// a 2103-chunk document (the largest seen in practice as of this change)
// down to a single statement, not many.
const chunkBulkCreateRowsPerStatement = 2000

// BulkCreate persists chunks in a single transaction.
//
// Uses one multi-row "INSERT ... VALUES (...), (...), ..." statement per
// chunkBulkCreateRowsPerStatement-sized group rather than one Exec per
// row -- see entity.pgstore.BulkCreate's doc for the full story on why
// (measured ~45ms/row against a real remote Postgres from a naive per-row
// Exec loop, a genuine per-round-trip cost, not connection warm-up).
// Chunks hit this same pattern too, and land squarely on a path entities
// don't: this is the very last step of document indexing
// (regionhandler.go), run after both /regions and /embeddings complete
// and directly gating when a document flips to StatusIndexed -- the
// user-facing "ready to search" signal, unlike entity extraction. For a
// 2103-chunk document that's roughly another 1.5 minutes of pure insert
// overhead previously baked silently into every large document's
// indexing time.
//
// Deliberately NOT CopyFrom (Postgres's COPY protocol, used for
// entity.pgstore's version of this exact fix): COPY has no per-column
// cast syntax, and the embedding column needs one ("$N::vector") for
// pgx to encode a plain Go string as a vector literal correctly --
// tested directly against a real pgvector column, and passing the
// uncast value through CopyFrom does NOT just fall back to text parsing
// the way Postgres's own COPY FROM STDIN would; pgx's own encoding path
// misencodes it, provoking "vector cannot have more than 16000
// dimensions" for a value that was actually 384-dimensional. A
// multi-row VALUES INSERT keeps the exact same $N::vector cast the
// original single-row version already used successfully, just batched.
func (s *Store) BulkCreate(ctx context.Context, chunks []*chunk.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		for start := 0; start < len(chunks); start += chunkBulkCreateRowsPerStatement {
			end := start + chunkBulkCreateRowsPerStatement
			if end > len(chunks) {
				end = len(chunks)
			}
			group := chunks[start:end]

			placeholders := make([]string, len(group))
			args := make([]any, 0, len(group)*chunkBulkCreateColumnCount)
			for i, c := range group {
				var bboxJSON []byte
				if c.BoundingBox != nil {
					var err error
					bboxJSON, err = json.Marshal(c.BoundingBox)
					if err != nil {
						return fmt.Errorf("marshal bounding_box: %w", err)
					}
				}
				base := i * chunkBulkCreateColumnCount
				placeholders[i] = fmt.Sprintf(
					"($%d,$%d,$%d,$%d,$%d,$%d,$%d::vector,$%d,$%d,$%d,$%d,$%d)",
					base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11, base+12,
				)
				args = append(args,
					c.DocumentID, c.KBID, c.UserID, c.Ordinal,
					c.Text, c.TokenCount, vectorParam(c.Embedding), c.CharStart, c.CharEnd,
					c.RegionID, c.PageNumber, bboxJSON,
				)
			}

			query := "INSERT INTO chunks (" + chunkBulkCreateColumns + ") VALUES " + strings.Join(placeholders, ",")
			if _, err := tx.Exec(ctx, query, args...); err != nil {
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

// UpdateEmbedding sets the embedding vector for the chunk id owned by
// userID. Returns an error if no such chunk exists for that tenant.
func (s *Store) UpdateEmbedding(ctx context.Context, userID, id uuid.UUID, embedding []float32) error {
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		tag, txErr := tx.Exec(ctx,
			`UPDATE chunks SET embedding = $1::vector WHERE id = $2 AND user_id = $3`,
			vectorParam(embedding), id, userID,
		)
		if txErr != nil {
			return txErr
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("no chunk %s for user %s", id, userID)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("chunk: update embedding %v: %w", id, err)
	}
	return nil
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
