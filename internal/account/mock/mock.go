// Package mock provides test doubles for account.AccountDeleter.
package mock

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/account"
	"github.com/kunalpednekar/dumpster/internal/session"
)

// Deleter is an in-memory test double for account.AccountDeleter, used by
// Sweep tests so they don't need to drive a real object-store / DB chain.
type Deleter struct {
	mu      sync.Mutex
	deleted []uuid.UUID
	// ErrFor, when set for a session ID, is returned by Delete for that session.
	ErrFor map[uuid.UUID]error
}

// NewDeleter returns a Deleter ready for use in tests.
func NewDeleter() *Deleter {
	return &Deleter{}
}

// Delete records the deletion and returns any configured error for s.ID.
func (d *Deleter) Delete(_ context.Context, s *session.Session) error {
	if err, ok := d.ErrFor[s.ID]; ok {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deleted = append(d.deleted, s.ID)
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

// DeletedCount returns the number of sessions Delete was called for.
func (d *Deleter) DeletedCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.deleted)
}

var _ account.AccountDeleter = (*Deleter)(nil)
