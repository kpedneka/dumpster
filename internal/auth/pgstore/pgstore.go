// Package pgstore provides a Postgres-backed implementation of
// auth.LocalUserStore, mapping external Clerk user identities onto the
// app's own UUID-keyed users table.
package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kunalpednekar/dumpster/internal/auth"
)

// Store is a Postgres-backed auth.LocalUserStore.
type Store struct {
	pool *pgxpool.Pool
}

// New returns an auth.LocalUserStore backed by Postgres.
func New(pool *pgxpool.Pool) auth.LocalUserStore {
	return &Store{pool: pool}
}

// GetOrCreateByClerkID returns the local user mapped to clerkUserID,
// inserting a new row (with a server-set created_at) the first time this
// identity is seen. The insert is a single atomic upsert — on conflict the
// existing row is returned unchanged via the no-op DO UPDATE — so
// concurrent first requests for the same identity can't race each other
// into duplicate rows or a missed RETURNING.
func (s *Store) GetOrCreateByClerkID(ctx context.Context, clerkUserID, email string) (*auth.User, error) {
	var u auth.User
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (clerk_user_id, email) VALUES ($1, $2)
		 ON CONFLICT (clerk_user_id) DO UPDATE SET clerk_user_id = users.clerk_user_id
		 RETURNING id, clerk_user_id, email, created_at`,
		clerkUserID, email,
	).Scan(&u.ID, &u.ClerkUserID, &u.Email, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("pgstore: get or create user: %w", err)
	}
	return &u, nil
}

// GetByClerkID looks up a local user by their external Clerk ID.
func (s *Store) GetByClerkID(ctx context.Context, clerkUserID string) (*auth.User, error) {
	var u auth.User
	err := s.pool.QueryRow(ctx,
		`SELECT id, clerk_user_id, email, created_at FROM users WHERE clerk_user_id = $1`,
		clerkUserID,
	).Scan(&u.ID, &u.ClerkUserID, &u.Email, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrUserNotFound
		}
		return nil, fmt.Errorf("pgstore: get user by clerk id: %w", err)
	}
	return &u, nil
}

// GetByID looks up a local user by their internal UUID.
func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*auth.User, error) {
	var u auth.User
	err := s.pool.QueryRow(ctx,
		`SELECT id, clerk_user_id, email, created_at FROM users WHERE id = $1`,
		id,
	).Scan(&u.ID, &u.ClerkUserID, &u.Email, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrUserNotFound
		}
		return nil, fmt.Errorf("pgstore: get user by id: %w", err)
	}
	return &u, nil
}

// DeleteByClerkID removes the local user mapped to clerkUserID, if any.
// A future card may instead soft-delete or cascade-clean tenant data on
// account deletion; for now this satisfies "webhooks wired but not yet
// acted on" by providing the primitive without invoking it automatically.
func (s *Store) DeleteByClerkID(ctx context.Context, clerkUserID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM users WHERE clerk_user_id = $1`, clerkUserID)
	if err != nil {
		return fmt.Errorf("pgstore: delete user by clerk id: %w", err)
	}
	return nil
}

var _ auth.LocalUserStore = (*Store)(nil)
