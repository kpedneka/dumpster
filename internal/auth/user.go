package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// User is the local identity record mapped to an external Clerk user.
// Internal tables continue to key off ID (the app's own UUID scheme);
// ClerkUserID is the external identity Clerk authenticates against.
type User struct {
	ID          uuid.UUID
	ClerkUserID string
	Email       string
	CreatedAt   time.Time
	// WarningSentAt is when the day-6 demo-account TTL warning email was
	// sent, or nil if it hasn't been sent yet. Set by the account lifecycle
	// sweep (internal/account.Sweep) to make repeated sweep runs idempotent.
	WarningSentAt *time.Time
}

// LocalUserStore is the persistence boundary for the local user records
// that map an external Clerk identity to the app's internal UUID scheme.
// Every other domain table's user_id column refers to User.ID, never to
// ClerkUserID directly, so the rest of the schema is unaffected by the
// switch to Clerk-managed authentication.
type LocalUserStore interface {
	// GetOrCreateByClerkID returns the local user mapped to clerkUserID,
	// creating one (with a freshly generated, server-set CreatedAt) on
	// first sight of that identity.
	GetOrCreateByClerkID(ctx context.Context, clerkUserID, email string) (*User, error)
	// GetByClerkID looks up a local user by their external Clerk ID without
	// creating one. Returns ErrUserNotFound if no mapping exists.
	GetByClerkID(ctx context.Context, clerkUserID string) (*User, error)
	// GetByID looks up a local user by their internal UUID.
	GetByID(ctx context.Context, id uuid.UUID) (*User, error)
	// DeleteByClerkID removes the local user mapped to clerkUserID, if any.
	// Used by webhook-driven lifecycle handling; a no-op if no mapping exists.
	DeleteByClerkID(ctx context.Context, clerkUserID string) error
	// ListForSweep returns every local user, with no tenant filter. This is
	// the one legitimate cross-tenant query in the app: it's used solely by
	// the daily account-lifecycle sweep, which operates on the tenant
	// membership table (users) itself rather than tenant-owned domain data.
	ListForSweep(ctx context.Context) ([]*User, error)
	// MarkWarningSent stamps WarningSentAt = now for id, idempotently.
	MarkWarningSent(ctx context.Context, id uuid.UUID) error
}
