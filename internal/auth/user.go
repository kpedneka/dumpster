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
}
