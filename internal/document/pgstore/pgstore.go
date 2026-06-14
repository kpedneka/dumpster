// Package pgstore provides a Postgres-backed document.Repository.
package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/document"
)

type Store struct {
	runner db.TxRunner
}

func New(runner db.TxRunner) document.Repository {
	return &Store{runner: runner}
}

func (s *Store) Create(ctx context.Context, d *document.Document) (*document.Document, error) {
	var result document.Document
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO documents (kb_id, user_id, filename, s3_key, content_type, status)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 RETURNING id, kb_id, user_id, filename, s3_key, content_type, status, created_at, updated_at`,
			d.KBID, d.UserID, d.Filename, d.S3Key, d.ContentType, string(d.Status),
		).Scan(
			&result.ID, &result.KBID, &result.UserID,
			&result.Filename, &result.S3Key, &result.ContentType,
			&result.Status, &result.CreatedAt, &result.UpdatedAt,
		)
	})
	if err != nil {
		return nil, fmt.Errorf("document: create: %w", err)
	}
	return &result, nil
}

func (s *Store) Get(ctx context.Context, userID, id int64) (*document.Document, error) {
	var result document.Document
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, kb_id, user_id, filename, s3_key, content_type, status, created_at, updated_at
			 FROM documents WHERE id = $1 AND user_id = $2`,
			id, userID,
		).Scan(
			&result.ID, &result.KBID, &result.UserID,
			&result.Filename, &result.S3Key, &result.ContentType,
			&result.Status, &result.CreatedAt, &result.UpdatedAt,
		)
	})
	if err != nil {
		return nil, fmt.Errorf("document: get %d: %w", id, err)
	}
	return &result, nil
}

func (s *Store) ListByKB(ctx context.Context, userID, kbID int64) ([]*document.Document, error) {
	var results []*document.Document
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, kb_id, user_id, filename, s3_key, content_type, status, created_at, updated_at
			 FROM documents WHERE kb_id = $1 AND user_id = $2 ORDER BY created_at DESC`,
			kbID, userID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d document.Document
			if err := rows.Scan(
				&d.ID, &d.KBID, &d.UserID,
				&d.Filename, &d.S3Key, &d.ContentType,
				&d.Status, &d.CreatedAt, &d.UpdatedAt,
			); err != nil {
				return err
			}
			results = append(results, &d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("document: list by kb %d: %w", kbID, err)
	}
	return results, nil
}

func (s *Store) UpdateStatus(ctx context.Context, userID, id int64, status document.Status) error {
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE documents SET status = $1, updated_at = NOW()
			 WHERE id = $2 AND user_id = $3`,
			string(status), id, userID,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("document: update status %d: %w", id, err)
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, userID, id int64) error {
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM documents WHERE id = $1 AND user_id = $2`,
			id, userID,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("document: delete %d: %w", id, err)
	}
	return nil
}

var _ document.Repository = (*Store)(nil)
