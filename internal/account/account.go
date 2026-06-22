// Package account implements the demo account lifecycle: a hard-delete
// capability spanning Clerk, R2, and the local DB, plus (in sweep.go) the
// day-6/day-7 TTL sweep that drives it.
package account

import (
	"context"
	"errors"
	"fmt"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
)

// IdentityDeleter removes a user's external identity. Implemented by
// internal/auth/clerk.Deleter; defined here so account.Deleter depends on
// an interface rather than the Clerk SDK directly.
type IdentityDeleter interface {
	DeleteUser(ctx context.Context, clerkUserID string) error
}

// Deleter performs the per-user hard delete: every R2 object the user owns,
// their Clerk identity, then their local DB row.
type Deleter struct {
	identity IdentityDeleter
	objects  objectstore.ObjectStore
	docs     document.Repository
	users    auth.LocalUserStore
}

// New returns a Deleter wired to the given stores.
func New(identity IdentityDeleter, objects objectstore.ObjectStore, docs document.Repository, users auth.LocalUserStore) *Deleter {
	return &Deleter{identity: identity, objects: objects, docs: docs, users: users}
}

// Delete hard-deletes u: every R2 object it owns, its Clerk identity, then
// its local DB row, in that order. The local row is deleted last so that a
// failure earlier in the sequence leaves it in place for a retry, rather
// than orphaning Clerk/R2 state with no record left to retry against.
func (d *Deleter) Delete(ctx context.Context, u *auth.User) error {
	ctx = auth.WithUserID(ctx, u.ID)

	docs, err := d.docs.ListByUserID(ctx, u.ID)
	if err != nil {
		return fmt.Errorf("account: list documents for %s: %w", u.ID, err)
	}

	var objErrs []error
	for _, doc := range docs {
		if err := d.objects.Delete(ctx, doc.S3Key); err != nil {
			objErrs = append(objErrs, fmt.Errorf("delete object %q: %w", doc.S3Key, err))
		}
	}
	if len(objErrs) > 0 {
		return fmt.Errorf("account: delete %s: %w", u.ID, errors.Join(objErrs...))
	}

	if err := d.identity.DeleteUser(ctx, u.ClerkUserID); err != nil {
		return fmt.Errorf("account: delete clerk identity for %s: %w", u.ID, err)
	}

	if err := d.users.DeleteByClerkID(ctx, u.ClerkUserID); err != nil {
		return fmt.Errorf("account: delete local user %s: %w", u.ID, err)
	}

	return nil
}
