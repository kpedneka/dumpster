// Package session defines the anonymous session identity that replaces
// Clerk-managed authentication. Every request either presents a valid session
// cookie or receives a freshly minted one; the session UUID is the tenant
// identifier that drives RLS and every domain repository's user_id column.
package session

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrSessionNotFound is returned when a session lookup finds no matching row,
// including the case where the session existed but has already been swept.
var ErrSessionNotFound = errors.New("session: not found")

// Session is a server-minted anonymous identity. Its ID is used everywhere
// the rest of the codebase expects a user_id: RLS policies, repository
// queries, and the auth context contract (auth.WithUserID /
// auth.UserIDFromContext) are all unchanged.
type Session struct {
	ID           uuid.UUID
	CreatedAt    time.Time
	LastActiveAt time.Time
	// WarnedAt is set by the account lifecycle sweep on the day-6 TTL
	// boundary. Nil means the session has not yet been warned.
	WarnedAt *time.Time
}

// SessionStore is the persistence boundary for anonymous sessions.
// Implementations live in dedicated adapter packages (session/pgstore,
// session/mock) so no storage driver is imported here.
type SessionStore interface {
	// Create mints a new session row and returns it.
	Create(ctx context.Context) (*Session, error)
	// Touch updates last_active_at for id to now. Non-fatal if the session
	// no longer exists (concurrent sweep); callers should log and proceed.
	Touch(ctx context.Context, id uuid.UUID, now time.Time) error
	// GetByID retrieves the session for id. Returns ErrSessionNotFound when
	// the session does not exist or has already been swept and deleted.
	GetByID(ctx context.Context, id uuid.UUID) (*Session, error)
	// ListForSweep returns every session with no tenant filter. The one
	// legitimate cross-tenant query: used solely by the daily account
	// lifecycle sweep that operates on the sessions table itself.
	ListForSweep(ctx context.Context) ([]*Session, error)
	// MarkWarned stamps warned_at = now for id, idempotently.
	MarkWarned(ctx context.Context, id uuid.UUID) error
	// Delete removes the session row for id. Used by the account lifecycle
	// sweep after all tenant-owned data has been cleaned up; the cascading
	// FK on knowledge_bases / documents / chunks removes those rows too.
	Delete(ctx context.Context, id uuid.UUID) error
}
