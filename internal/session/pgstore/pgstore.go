// Package pgstore provides a Postgres-backed implementation of
// session.SessionStore, reading from and writing to the sessions table.
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kunalpednekar/dumpster/internal/session"
)

// Store is a Postgres-backed session.SessionStore.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a session.SessionStore backed by Postgres.
func New(pool *pgxpool.Pool) session.SessionStore {
	return &Store{pool: pool}
}

// Create inserts a new session row and returns it.
func (s *Store) Create(ctx context.Context) (*session.Session, error) {
	var sess session.Session
	err := s.pool.QueryRow(ctx,
		`INSERT INTO sessions DEFAULT VALUES
		 RETURNING id, created_at, last_active_at, warned_at`,
	).Scan(&sess.ID, &sess.CreatedAt, &sess.LastActiveAt, &sess.WarnedAt)
	if err != nil {
		return nil, fmt.Errorf("pgstore: create session: %w", err)
	}
	return &sess, nil
}

// Touch updates last_active_at for id to now.
func (s *Store) Touch(ctx context.Context, id uuid.UUID, now time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE sessions SET last_active_at = $1 WHERE id = $2`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("pgstore: touch session %s: %w", id, err)
	}
	return nil
}

// GetByID retrieves the session for id.
func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*session.Session, error) {
	var sess session.Session
	err := s.pool.QueryRow(ctx,
		`SELECT id, created_at, last_active_at, warned_at FROM sessions WHERE id = $1`,
		id,
	).Scan(&sess.ID, &sess.CreatedAt, &sess.LastActiveAt, &sess.WarnedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, session.ErrSessionNotFound
		}
		return nil, fmt.Errorf("pgstore: get session %s: %w", id, err)
	}
	return &sess, nil
}

// ListForSweep returns all sessions with no tenant filter.
func (s *Store) ListForSweep(ctx context.Context) ([]*session.Session, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, created_at, last_active_at, warned_at FROM sessions`,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list sessions for sweep: %w", err)
	}
	defer rows.Close()

	var sessions []*session.Session
	for rows.Next() {
		var sess session.Session
		if err := rows.Scan(&sess.ID, &sess.CreatedAt, &sess.LastActiveAt, &sess.WarnedAt); err != nil {
			return nil, fmt.Errorf("pgstore: list sessions for sweep: %w", err)
		}
		sessions = append(sessions, &sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore: list sessions for sweep: %w", err)
	}
	return sessions, nil
}

// MarkWarned stamps warned_at = now for id, idempotently.
func (s *Store) MarkWarned(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE sessions SET warned_at = NOW() WHERE id = $1 AND warned_at IS NULL`,
		id,
	)
	if err != nil {
		return fmt.Errorf("pgstore: mark warned for %s: %w", id, err)
	}
	return nil
}

// Delete removes the session row for id.
func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("pgstore: delete session %s: %w", id, err)
	}
	return nil
}

var _ session.SessionStore = (*Store)(nil)
