// Package mock provides test doubles for account.IdentityDeleter and
// account.AccountDeleter.
package mock

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/account"
	"github.com/kunalpednekar/dumpster/internal/auth"
)

// IdentityDeleter is an in-memory test double for account.IdentityDeleter.
type IdentityDeleter struct {
	mu      sync.Mutex
	deleted []string
	// Err, when set, is returned by every DeleteUser call.
	Err error
}

func New() *IdentityDeleter {
	return &IdentityDeleter{}
}

func (d *IdentityDeleter) DeleteUser(_ context.Context, clerkUserID string) error {
	if d.Err != nil {
		return d.Err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deleted = append(d.deleted, clerkUserID)
	return nil
}

// Deleted reports whether DeleteUser was called with clerkUserID.
func (d *IdentityDeleter) Deleted(clerkUserID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, id := range d.deleted {
		if id == clerkUserID {
			return true
		}
	}
	return false
}

var _ account.IdentityDeleter = (*IdentityDeleter)(nil)

// Deleter is an in-memory test double for account.AccountDeleter, used by
// Sweep tests so they don't need to drive a real Deleter's Clerk/R2/DB
// chain.
type Deleter struct {
	mu      sync.Mutex
	deleted []uuid.UUID
	// ErrFor, when set for a user ID, is returned by Delete for that user.
	ErrFor map[uuid.UUID]error
}

func NewDeleter() *Deleter {
	return &Deleter{}
}

func (d *Deleter) Delete(_ context.Context, u *auth.User) error {
	if err, ok := d.ErrFor[u.ID]; ok {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deleted = append(d.deleted, u.ID)
	return nil
}

// Deleted reports whether Delete was called for id.
func (d *Deleter) Deleted(id uuid.UUID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, got := range d.deleted {
		if got == id {
			return true
		}
	}
	return false
}

// DeletedCount returns how many users Delete was called for.
func (d *Deleter) DeletedCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.deleted)
}

var _ account.AccountDeleter = (*Deleter)(nil)
