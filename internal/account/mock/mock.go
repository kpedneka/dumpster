// Package mock provides a test double for account.IdentityDeleter.
package mock

import (
	"context"
	"sync"

	"github.com/kunalpednekar/dumpster/internal/account"
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
