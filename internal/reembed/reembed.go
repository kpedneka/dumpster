// Package reembed re-embeds every chunk missing a vector, across every
// tenant. It exists to backfill chunks left behind by an embedding-model
// migration (a changed vector dimension nulls out incompatible vectors —
// see migrations/019_local_embeddings.sql), driven by cmd/reembed.
//
// Cross-tenant reach is achieved by iterating every session (the tenant
// identity throughout this codebase) via session.SessionStore.ListForSweep
// — the same "one legitimate cross-tenant query" pattern the account
// lifecycle sweep uses — and then running all per-tenant work through the
// normal tenant-scoped Repository methods under that session's identity.
// No query here bypasses RLS: chunks carries FORCE ROW LEVEL SECURITY, so a
// query issued without a tenant identity in scope would simply fail.
package reembed

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/kb"
	"github.com/kunalpednekar/dumpster/internal/llm"
	"github.com/kunalpednekar/dumpster/internal/session"
)

// defaultBatchSize is how many chunk texts are sent to the embedder in one
// call. Kept well under typical HTTP body/timeout limits while still
// amortizing the inference service's per-request overhead across many chunks.
const defaultBatchSize = 64

// Result summarizes one Run call.
type Result struct {
	Reembedded int
	Errors     []error
}

// Reembed re-embeds every pending (nil-embedding) chunk across all tenants.
type Reembed struct {
	sessions session.SessionStore
	kbs      kb.Repository
	chunks   chunk.Repository
	embedder llm.Embedder
	// BatchSize overrides defaultBatchSize; tests use a small value to
	// exercise multi-batch behavior without needing hundreds of chunks.
	BatchSize int
}

// New returns a Reembed wired to the given stores and embedder. embedder
// must be a document embedder (is_query: false) — see
// internal/llm/inference.NewDocumentEmbedder.
func New(sessions session.SessionStore, kbs kb.Repository, chunks chunk.Repository, embedder llm.Embedder) *Reembed {
	return &Reembed{sessions: sessions, kbs: kbs, chunks: chunks, embedder: embedder}
}

func (r *Reembed) batchSize() int {
	if r.BatchSize > 0 {
		return r.BatchSize
	}
	return defaultBatchSize
}

// Run scans every tenant's knowledge bases for chunks with no embedding,
// re-embeds them via embedder, and persists the result. Per-chunk-batch
// errors are collected into the result rather than aborting the rest of
// the backfill, so one bad batch or one unreachable tenant doesn't block
// progress on everything else.
func (r *Reembed) Run(ctx context.Context) (Result, error) {
	sessions, err := r.sessions.ListForSweep(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("reembed: list sessions: %w", err)
	}

	var result Result
	for _, sess := range sessions {
		tenantCtx := auth.WithUserID(ctx, sess.ID)

		kbs, err := r.kbs.List(tenantCtx, sess.ID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("list knowledge bases for session %s: %w", sess.ID, err))
			continue
		}

		for _, k := range kbs {
			r.reembedKB(tenantCtx, sess.ID, k.ID, &result)
		}
	}
	return result, nil
}

// reembedKB re-embeds every pending chunk in one knowledge base, appending
// any errors encountered to result.
func (r *Reembed) reembedKB(ctx context.Context, userID, kbID uuid.UUID, result *Result) {
	chunks, err := r.chunks.ListByKB(ctx, userID, kbID)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("list chunks for kb %s: %w", kbID, err))
		return
	}

	var pending []*chunk.Chunk
	for _, c := range chunks {
		if len(c.Embedding) == 0 {
			pending = append(pending, c)
		}
	}

	for start := 0; start < len(pending); start += r.batchSize() {
		end := min(start+r.batchSize(), len(pending))
		r.reembedBatch(ctx, userID, pending[start:end], result)
	}
}

// reembedBatch embeds one batch's texts in a single call and persists each
// resulting vector, appending any errors encountered to result.
func (r *Reembed) reembedBatch(ctx context.Context, userID uuid.UUID, batch []*chunk.Chunk, result *Result) {
	texts := make([]string, len(batch))
	for i, c := range batch {
		texts[i] = c.Text
	}

	vecs, err := r.embedder.Embed(ctx, texts)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("embed batch: %w", err))
		return
	}
	if len(vecs) != len(batch) {
		result.Errors = append(result.Errors, fmt.Errorf("embed batch: got %d vectors for %d chunks", len(vecs), len(batch)))
		return
	}

	for i, c := range batch {
		if err := r.chunks.UpdateEmbedding(ctx, userID, c.ID, vecs[i]); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("update embedding for chunk %s: %w", c.ID, err))
			continue
		}
		result.Reembedded++
	}
}
