// Package pgstore provides a Postgres-backed implementation of auth.UserStore.
package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kunalpednekar/dumpster/internal/auth"
)

type Store struct {
	pool *pgxpool.Pool
}

// New returns an auth.UserStore backed by Postgres.
func New(pool *pgxpool.Pool) auth.UserStore {
	return &Store{pool: pool}
}

func (s *Store) Create(ctx context.Context, email, passwordHash string) (*auth.User, error) {
	var u auth.User
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2)
		 RETURNING id, email, password_hash, created_at`,
		email, passwordHash,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("pgstore: create user: %w", err)
	}
	return &u, nil
}

func (s *Store) GetByEmail(ctx context.Context, email string) (*auth.User, error) {
	var u auth.User
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, created_at FROM users WHERE email = $1`,
		email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("pgstore: get user by email: %w", err)
	}
	return &u, nil
}

func (s *Store) GetByID(ctx context.Context, id int64) (*auth.User, error) {
	var u auth.User
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, created_at FROM users WHERE id = $1`,
		id,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("pgstore: get user by id: %w", err)
	}
	return &u, nil
}

// Compile-time check.
var _ auth.UserStore = (*Store)(nil)
