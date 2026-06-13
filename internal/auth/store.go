package auth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type pgUserStore struct {
	pool *pgxpool.Pool
}

// NewPgUserStore returns a UserStore backed by Postgres.
func NewPgUserStore(pool *pgxpool.Pool) UserStore {
	return &pgUserStore{pool: pool}
}

func (s *pgUserStore) Create(ctx context.Context, email, passwordHash string) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2)
		 RETURNING id, email, password_hash, created_at`,
		email, passwordHash,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("auth: create user: %w", err)
	}
	return &u, nil
}

func (s *pgUserStore) GetByEmail(ctx context.Context, email string) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, created_at FROM users WHERE email = $1`,
		email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("auth: get user by email: %w", err)
	}
	return &u, nil
}

func (s *pgUserStore) GetByID(ctx context.Context, id int64) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, created_at FROM users WHERE id = $1`,
		id,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("auth: get user by id: %w", err)
	}
	return &u, nil
}

// Compile-time check.
var _ UserStore = (*pgUserStore)(nil)
