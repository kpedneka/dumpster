// Package pgstore provides a Postgres-backed kb.Repository.
package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/kunalpednekar/dumpster/internal/db"
	"github.com/kunalpednekar/dumpster/internal/kb"
)

type Store struct {
	runner db.TxRunner
}

func New(runner db.TxRunner) kb.Repository {
	return &Store{runner: runner}
}

func (s *Store) Create(ctx context.Context, userID uuid.UUID, name string) (*kb.KnowledgeBase, error) {
	var result kb.KnowledgeBase
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO knowledge_bases (user_id, name)
			 VALUES ($1, $2)
			 RETURNING id, user_id, name, created_at, updated_at`,
			userID, name,
		).Scan(&result.ID, &result.UserID, &result.Name, &result.CreatedAt, &result.UpdatedAt)
	})
	if err != nil {
		return nil, fmt.Errorf("kb: create: %w", err)
	}
	return &result, nil
}

func (s *Store) Get(ctx context.Context, userID, id uuid.UUID) (*kb.KnowledgeBase, error) {
	var result kb.KnowledgeBase
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, user_id, name, created_at, updated_at
			 FROM knowledge_bases WHERE id = $1 AND user_id = $2`,
			id, userID,
		).Scan(&result.ID, &result.UserID, &result.Name, &result.CreatedAt, &result.UpdatedAt)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, kb.ErrNotFound
		}
		return nil, fmt.Errorf("kb: get %v: %w", id, err)
	}
	return &result, nil
}

func (s *Store) List(ctx context.Context, userID uuid.UUID) ([]*kb.KnowledgeBase, error) {
	var results []*kb.KnowledgeBase
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, user_id, name, created_at, updated_at
			 FROM knowledge_bases WHERE user_id = $1 ORDER BY created_at DESC`,
			userID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k kb.KnowledgeBase
			if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.CreatedAt, &k.UpdatedAt); err != nil {
				return err
			}
			results = append(results, &k)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("kb: list: %w", err)
	}
	return results, nil
}

func (s *Store) Rename(ctx context.Context, userID, id uuid.UUID, name string) (*kb.KnowledgeBase, error) {
	var result kb.KnowledgeBase
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`UPDATE knowledge_bases SET name = $1, updated_at = NOW()
			 WHERE id = $2 AND user_id = $3
			 RETURNING id, user_id, name, created_at, updated_at`,
			name, id, userID,
		).Scan(&result.ID, &result.UserID, &result.Name, &result.CreatedAt, &result.UpdatedAt)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, kb.ErrNotFound
		}
		return nil, fmt.Errorf("kb: rename %v: %w", id, err)
	}
	return &result, nil
}

func (s *Store) Delete(ctx context.Context, userID, id uuid.UUID) error {
	err := s.runner.RunInTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM knowledge_bases WHERE id = $1 AND user_id = $2`,
			id, userID,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return kb.ErrNotFound
		}
		return nil
	})
	if err != nil && !errors.Is(err, kb.ErrNotFound) {
		return fmt.Errorf("kb: delete %v: %w", id, err)
	}
	return err
}

var _ kb.Repository = (*Store)(nil)
