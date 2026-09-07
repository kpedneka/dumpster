package worker

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/canonical"
	"github.com/kunalpednekar/dumpster/internal/crosslink"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/entity"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// defaultMaxCrossLinkCandidatesPerRun bounds how many KB-wide cross-chunk
// candidate chains one document's canonicalization job also reviews,
// keeping LLM cost per ingestion predictable regardless of how large a
// KB's backlog gets -- same "0 means use the default" idiom as
// relationhandler.go's defaultMaxRelationChunksPerRun, and the same
// number cmd/crosslink's own -limit flag already defaults to. Cross-chunk
// linking is additive and incremental (see crosslink.Repository.
// CandidateChains), so working through a KB's full backlog is a matter of
// more documents landing over time, not raising this constant.
const defaultMaxCrossLinkCandidatesPerRun = 20

// CanonicalizationHandler resolves a document's entity mentions to stable
// canonical entities (internal/canonical), giving query-time graph
// traversal a cross-chunk, cross-document identity to hop on. It is a
// distinct job stage from EdgeHandler: it reads entity rows that entity
// extraction has already produced and never re-extracts or touches
// entity_edges, so it can be re-run independently.
//
// Alias resolution and cross-chunk linking both ride along on this same
// job stage, run automatically after every document instead of requiring
// someone to invoke cmd/aliasresolve or cmd/crosslink by hand -- see the
// "Fold cross-chunk linking + alias merging into automatic ingestion" dev
// board card. This is deliberately synchronous within one job stage, not
// a separate queued step or a periodic sweep: both still call a hosted
// LLM API directly today (no AWS Batch cold start to amortize), so
// there's no cost reason to decouple them yet. That reasoning is specific
// to the current backend -- if either moves to self-hosted GPU compute,
// reconsider inline-vs-sweep at that point, not before.
type CanonicalizationHandler struct {
	docs      document.Repository
	entities  entity.Repository
	canonical canonical.Repository
	// aliasJudge, when set, runs canonical.ResolveAliases after exact-match
	// resolution -- merging a differently-worded alias ("Kade" into
	// "Rosalind Kade") that exact-match canonicalization structurally can
	// never catch on its own. Optional: nil disables alias resolution
	// entirely, leaving exact-match-only behavior unchanged, the same
	// "dial room" precedent as relation_type being nullable.
	aliasJudge canonical.AliasJudge
	// crossLinkRepo/crossLinker, when both set, run cross-chunk linking
	// against kbID's candidate backlog after alias resolution -- creating a
	// genuinely new edge between canonical entities connected only by a
	// chain longer than graphrag.TraversalLeg's 2-hop expansion can reach.
	// Optional, same "nil disables it" precedent as aliasJudge. Must run
	// after alias resolution, not before or concurrently: alias merging can
	// delete the losing side of a merge, and cross-chunk linking must see
	// the settled canonical identities, not ones about to be merged away.
	crossLinkRepo  crosslink.Repository
	crossLinker    *crosslink.Extractor
	crossLinkLimit int
}

// NewCanonicalizationHandler creates a CanonicalizationHandler wired to the
// given dependencies.
func NewCanonicalizationHandler(docs document.Repository, entities entity.Repository, canonicalRepo canonical.Repository) *CanonicalizationHandler {
	return &CanonicalizationHandler{docs: docs, entities: entities, canonical: canonicalRepo}
}

// WithAliasJudge enables alias resolution (canonical.ResolveAliases) after
// every exact-match resolution this handler performs.
func (h *CanonicalizationHandler) WithAliasJudge(judge canonical.AliasJudge) *CanonicalizationHandler {
	h.aliasJudge = judge
	return h
}

// WithCrossLink enables cross-chunk linking after alias resolution, using
// repo to find candidate chains and extractor to confirm them. limit
// bounds how many candidates one job reviews; 0 uses
// defaultMaxCrossLinkCandidatesPerRun.
func (h *CanonicalizationHandler) WithCrossLink(repo crosslink.Repository, extractor *crosslink.Extractor, limit int) *CanonicalizationHandler {
	h.crossLinkRepo = repo
	h.crossLinker = extractor
	h.crossLinkLimit = limit
	return h
}

// Handle resolves canonical identities for one document's entity mentions.
// It only processes mentions not already linked to a canonical entity (see
// canonical.ResolveNew), so a redelivered job — e.g. after this handler
// succeeded but the queue never saw the ack — does not double-count.
func (h *CanonicalizationHandler) Handle(ctx context.Context, job *queue.Job) error {
	ctx = auth.WithUserID(ctx, job.UserID)

	doc, err := h.docs.Get(ctx, job.UserID, job.DocumentID)
	if err != nil {
		return fmt.Errorf("canonicalizationhandler: get document %s: %w", job.DocumentID, err)
	}

	mentions, err := h.entities.ListByDocument(ctx, job.UserID, job.DocumentID)
	if err != nil {
		return fmt.Errorf("canonicalizationhandler: list entities for document %s: %w", job.DocumentID, err)
	}
	if len(mentions) == 0 {
		return nil
	}

	resolved, err := canonical.ResolveNew(ctx, h.canonical, h.entities, job.UserID, mentions)
	if err != nil {
		return fmt.Errorf("canonicalizationhandler: resolve document %s: %w", job.DocumentID, err)
	}

	if h.aliasJudge != nil && len(resolved) > 0 {
		if err := canonical.ResolveAliases(ctx, h.canonical, job.UserID, doc.KBID, resolved, h.aliasJudge); err != nil {
			return fmt.Errorf("canonicalizationhandler: resolve aliases for document %s: %w", job.DocumentID, err)
		}
	}

	if h.crossLinkRepo != nil && h.crossLinker != nil {
		if err := h.runCrossLink(ctx, job.UserID, doc.KBID); err != nil {
			return fmt.Errorf("canonicalizationhandler: cross-chunk linking for document %s: %w", job.DocumentID, err)
		}
	}

	return nil
}

// runCrossLink reviews up to h.crossLinkLimit of kbID's cross-chunk
// candidate backlog and persists what the model confirms -- the same
// find-candidates/extract/apply-updates sequence cmd/crosslink runs by
// hand, now run automatically after every document that reaches
// canonicalization. A KB with nothing new to review (no candidates) is a
// normal, cheap no-op, not an error.
func (h *CanonicalizationHandler) runCrossLink(ctx context.Context, userID, kbID uuid.UUID) error {
	limit := h.crossLinkLimit
	if limit <= 0 {
		limit = defaultMaxCrossLinkCandidatesPerRun
	}

	candidates, err := h.crossLinkRepo.CandidateChains(ctx, userID, kbID, limit)
	if err != nil {
		return fmt.Errorf("find candidate chains: %w", err)
	}
	if len(candidates) == 0 {
		return nil
	}

	updates, err := h.crossLinker.Extract(ctx, candidates)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	if err := h.crossLinkRepo.ApplyUpdates(ctx, userID, kbID, updates); err != nil {
		return fmt.Errorf("apply updates: %w", err)
	}
	return nil
}

// OnFailed logs that canonicalization was permanently dead-lettered for the
// document. It deliberately does not touch document.Status: canonicalization
// failures do not affect the document's chunking, embedding, or entity
// status, since this is an independent job stage.
func (h *CanonicalizationHandler) OnFailed(_ context.Context, job *queue.Job) {
	log.Printf("canonicalizationhandler: canonicalization permanently failed for document %s", job.DocumentID)
}

var _ Handler = (*CanonicalizationHandler)(nil)
