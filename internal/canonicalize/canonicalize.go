// Package canonicalize backfills canonical_entities for entity mentions
// that existed before canonicalization shipped
// (migrations/022_canonical_entities.sql), across every tenant. Driven by
// cmd/canonicalize; meant to be run once, by hand, shortly after that
// migration and the accompanying code deploy land — new mentions going
// forward are canonicalized as part of the normal ingestion pipeline (see
// internal/worker.CanonicalizationHandler), so this only ever has work to
// do once.
//
// Cross-tenant reach is achieved the same way internal/reembed's backfill
// achieves it: iterating every session via session.SessionStore.ListForSweep
// and running all per-tenant work through the normal tenant-scoped
// Repository methods under that session's identity. No query here bypasses
// RLS.
package canonicalize

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/canonical"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/entity"
	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/session"
)

// Result summarizes one Run call.
type Result struct {
	// Resolved is the number of entity mentions newly linked to a canonical
	// entity. Mentions already linked (e.g. a re-run after a partial
	// failure) are not recounted.
	Resolved int
	Errors   []error
}

// Backfill resolves canonical entities for every not-yet-canonicalized
// entity mention across all tenants.
type Backfill struct {
	sessions  session.SessionStore
	kbs       kb.Repository
	docs      document.Repository
	entities  entity.Repository
	canonical canonical.Repository
}

// New returns a Backfill wired to the given stores.
func New(sessions session.SessionStore, kbs kb.Repository, docs document.Repository, entities entity.Repository, canonicalRepo canonical.Repository) *Backfill {
	return &Backfill{sessions: sessions, kbs: kbs, docs: docs, entities: entities, canonical: canonicalRepo}
}

// Run scans every tenant's knowledge bases for documents with
// not-yet-canonicalized entity mentions, resolves them, and persists the
// result. Per-document errors are collected into the result rather than
// aborting the rest of the backfill, so one bad document or one unreachable
// tenant doesn't block progress on everything else.
func (b *Backfill) Run(ctx context.Context) (Result, error) {
	sessions, err := b.sessions.ListForSweep(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("canonicalize: list sessions: %w", err)
	}

	var result Result
	for _, sess := range sessions {
		tenantCtx := auth.WithUserID(ctx, sess.ID)

		kbs, err := b.kbs.List(tenantCtx, sess.ID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("list knowledge bases for session %s: %w", sess.ID, err))
			continue
		}

		for _, k := range kbs {
			b.backfillKB(tenantCtx, sess.ID, k.ID, &result)
		}
	}
	return result, nil
}

// backfillKB resolves canonical entities for every not-yet-canonicalized
// mention across every document in one knowledge base, appending any
// errors encountered to result.
func (b *Backfill) backfillKB(ctx context.Context, userID, kbID uuid.UUID, result *Result) {
	docs, err := b.docs.ListByKB(ctx, userID, kbID)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("list documents for kb %s: %w", kbID, err))
		return
	}

	for _, d := range docs {
		mentions, err := b.entities.ListByDocument(ctx, userID, d.ID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("list entities for document %s: %w", d.ID, err))
			continue
		}

		var pending int
		for _, m := range mentions {
			if m.CanonicalEntityID == nil {
				pending++
			}
		}
		if pending == 0 {
			continue
		}

		if _, err := canonical.ResolveNew(ctx, b.canonical, b.entities, userID, mentions); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("resolve canonical entities for document %s: %w", d.ID, err))
			continue
		}
		result.Resolved += pending
	}
}
