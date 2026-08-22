// Package mock provides an in-memory test double for session.SessionStore,
// following the function-pointer-field pattern used throughout the codebase.
package mock

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/session"
)

// Store is an in-memory session.SessionStore for use in tests.
type Store struct {
	mu sync.Mutex
	// sessions holds the live (non-deleted) sessions.
	sessions map[uuid.UUID]*session.Session

	// Touched tracks every id passed to Touch, in call order.
	Touched []uuid.UUID

	// CreateErr, when non-nil, is returned by every Create call.
	CreateErr error
}

// New returns an empty in-memory Store.
func New() *Store {
	return &Store{sessions: make(map[uuid.UUID]*session.Session)}
}

// Seed registers s directly in the store, bypassing Create. Used in tests
// to pre-create a session with a known ID and CreatedAt so that
// authedRequest can present a cookie that resolves to that exact identity.
func (s *Store) Seed(sess *session.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
}

// Create mints a fresh session with a random ID.
func (s *Store) Create(_ context.Context) (*session.Session, error) {
	if s.CreateErr != nil {
		return nil, s.CreateErr
	}
	sess := &session.Session{
		ID:           uuid.New(),
		CreatedAt:    time.Now(),
		LastActiveAt: time.Now(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
	return sess, nil
}

// Touch records the call and updates LastActiveAt.
func (s *Store) Touch(_ context.Context, id uuid.UUID, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Touched = append(s.Touched, id)
	if sess, ok := s.sessions[id]; ok {
		sess.LastActiveAt = now
	}
	return nil
}

// GetByID returns the session for id, or ErrSessionNotFound if absent.
func (s *Store) GetByID(_ context.Context, id uuid.UUID) (*session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	return sess, nil
}

// ListForSweep returns all live sessions.
func (s *Store) ListForSweep(_ context.Context) ([]*session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*session.Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	return out, nil
}

// MarkWarned stamps WarnedAt = now on the session, idempotently.
func (s *Store) MarkWarned(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return session.ErrSessionNotFound
	}
	if sess.WarnedAt == nil {
		now := time.Now()
		sess.WarnedAt = &now
	}
	return nil
}

// Delete removes the session from the store.
func (s *Store) Delete(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	return nil
}

var _ session.SessionStore = (*Store)(nil)
