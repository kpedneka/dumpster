// Package account implements the demo account lifecycle: a hard-delete
// capability spanning object storage and the local DB, plus (in sweep.go)
// the day-6/day-7 TTL sweep that drives it.
package account

import (
	"context"
	"errors"
	"fmt"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/session"
)

// AccountDeleter hard-deletes a session and all its data. Implemented by
// Deleter; Sweep depends on the interface rather than the concrete type so
// it can be tested without driving a real object-store / DB chain.
type AccountDeleter interface {
	Delete(ctx context.Context, s *session.Session) error
}

// Deleter performs the per-session hard delete: every object the session
// owns (from object storage), then the session row itself (which cascades
// to knowledge_bases, documents, and chunks via FK ON DELETE CASCADE).
type Deleter struct {
	sessions session.SessionStore
	objects  objectstore.ObjectStore
	docs     document.Repository
}

// New returns a Deleter wired to the given stores.
func New(sessions session.SessionStore, objects objectstore.ObjectStore, docs document.Repository) *Deleter {
	return &Deleter{sessions: sessions, objects: objects, docs: docs}
}

// Delete hard-deletes s: every object it owns from object storage, then
// the session row itself. The session row is deleted last so that a failure
// earlier in the sequence leaves it in place for a retry, rather than
// orphaning object-store state with no record left to retry against.
func (d *Deleter) Delete(ctx context.Context, s *session.Session) error {
	// Place session.ID in context so RLS-gated repositories can query this
	// session's data without a full-table scan violation.
	ctx = auth.WithUserID(ctx, s.ID)

	docs, err := d.docs.ListByUserID(ctx, s.ID)
	if err != nil {
		return fmt.Errorf("account: list documents for %s: %w", s.ID, err)
	}

	var objErrs []error
	for _, doc := range docs {
		if err := d.objects.Delete(ctx, doc.S3Key); err != nil {
			objErrs = append(objErrs, fmt.Errorf("delete object %q: %w", doc.S3Key, err))
		}
	}
	if len(objErrs) > 0 {
		return fmt.Errorf("account: delete %s: %w", s.ID, errors.Join(objErrs...))
	}

	if err := d.sessions.Delete(ctx, s.ID); err != nil {
		return fmt.Errorf("account: delete session %s: %w", s.ID, err)
	}

	return nil
}
