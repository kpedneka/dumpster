package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/canonical"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/entity"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Heartbeater is the narrow capability EntityHandler needs from the queue
// to protect a long-running, multi-batch job from a false staleness
// reclaim — see WithHeartbeat. queue.Consumer satisfies this.
type Heartbeater interface {
	Heartbeat(ctx context.Context, jobID uuid.UUID) error
}

// EntityHandler runs local entity extraction over a document's existing
// chunks. It is a distinct job stage from DocumentHandler: it reads chunks
// that chunking/embedding has already produced and never re-chunks or
// re-embeds, so it can be re-run independently — e.g. after the configured
// entity type set changes — without re-touching the document's vector or
// keyword representation.
type EntityHandler struct {
	docs      document.Repository
	chunks    chunk.Repository
	entities  entity.Repository
	extractor entity.Extractor
	// canonical reverses a document's prior contribution to canonical
	// entity stats before its stale entity rows are replaced — see
	// canonical.Repository.DecrementForDocument. Required: unlike edge
	// extraction, this must run synchronously in the same replace step,
	// since the mention -> canonical linkage it reads is gone once the old
	// entities rows are deleted.
	canonical canonical.Repository
	// allowedTypes is the config-driven, domain-agnostic entity type set
	// (see internal/config ENTITY_TYPES). Adding a type is a config change,
	// not a code change, and requires no migration.
	allowedTypes []entity.Type
	// publisher, when non-nil, is used to enqueue the distinct edge-
	// extraction and canonicalization jobs once entities are successfully
	// persisted. Optional so existing callers and tests that only care
	// about entity extraction keep working unmodified.
	publisher queue.Publisher
	// batchSize caps how many chunks are sent to the Extractor in one call.
	// 0 defaults to defaultEntityExtractionBatchSize.
	batchSize int
	// heartbeat, when non-nil, is called after each successful batch (or,
	// for a BatchExtractor, on heartbeatInterval while ExtractBatches's
	// concurrent batches are in flight) so ReclaimStale doesn't mistake a
	// long, actively-progressing document for a stuck one — a real job was
	// observed getting reclaimed and its attempt count burned mid-extraction
	// despite steadily persisting progress, purely because nothing touched
	// updated_at between batches. Optional so existing callers/tests keep
	// working unmodified.
	heartbeat Heartbeater
	// heartbeatInterval is how often startHeartbeatTicker fires. 0 defaults
	// to defaultEntityHeartbeatInterval; overridable via
	// WithHeartbeatInterval, mainly so tests can exercise the ticker
	// without waiting real minutes.
	heartbeatInterval time.Duration
}

// defaultEntityExtractionBatchSize applies when batchSize is unset — see
// WithBatchSize.
const defaultEntityExtractionBatchSize = 50

// defaultEntityHeartbeatInterval applies when heartbeatInterval is unset —
// see WithHeartbeatInterval. Comfortably inside JobStaleTimeout's default
// (15m) with real margin — this only needs to touch the job row often
// enough that ReclaimStale never mistakes actively-running work for a
// stuck job.
const defaultEntityHeartbeatInterval = 2 * time.Minute

// NewEntityHandler creates an EntityHandler wired to the given dependencies.
// allowedTypes is the entity type set read from config.
func NewEntityHandler(
	docs document.Repository,
	chunks chunk.Repository,
	entities entity.Repository,
	extractor entity.Extractor,
	canonicalRepo canonical.Repository,
	allowedTypes []string,
) *EntityHandler {
	types := make([]entity.Type, len(allowedTypes))
	for i, t := range allowedTypes {
		types[i] = entity.Type(t)
	}
	return &EntityHandler{
		docs:         docs,
		chunks:       chunks,
		entities:     entities,
		extractor:    extractor,
		canonical:    canonicalRepo,
		allowedTypes: types,
	}
}

// WithDownstreamPublisher wires publisher into h so that, after entities are
// successfully persisted, distinct edge-extraction and canonicalization jobs
// are queued for the same document. Mirrors DocumentHandler's
// WithEntityExtractionPublisher.
func (h *EntityHandler) WithDownstreamPublisher(publisher queue.Publisher) *EntityHandler {
	h.publisher = publisher
	return h
}

// WithBatchSize sets how many chunks are sent to the Extractor per call.
// Bounds worst-case single-request duration regardless of document size —
// see defaultEntityExtractionBatchSize's doc for why this matters.
func (h *EntityHandler) WithBatchSize(n int) *EntityHandler {
	h.batchSize = n
	return h
}

// WithHeartbeat wires heartbeater into h so that a long, multi-batch
// extraction keeps the job's staleness clock from firing on it while it's
// actively making progress. See the heartbeat field's doc for why.
func (h *EntityHandler) WithHeartbeat(heartbeater Heartbeater) *EntityHandler {
	h.heartbeat = heartbeater
	return h
}

// WithHeartbeatInterval overrides how often startHeartbeatTicker fires.
// See the heartbeatInterval field's doc.
func (h *EntityHandler) WithHeartbeatInterval(d time.Duration) *EntityHandler {
	h.heartbeatInterval = d
	return h
}

func (h *EntityHandler) heartbeatIntervalOrDefault() time.Duration {
	if h.heartbeatInterval <= 0 {
		return defaultEntityHeartbeatInterval
	}
	return h.heartbeatInterval
}

func (h *EntityHandler) batchSizeOrDefault() int {
	if h.batchSize <= 0 {
		return defaultEntityExtractionBatchSize
	}
	return h.batchSize
}

// Handle runs entity extraction for one document job: it fetches the
// document's existing chunks (without re-chunking or re-embedding them),
// runs the configured Extractor over them in batches (see WithBatchSize),
// persisting each batch's results as it completes rather than accumulating
// everything in memory until the end.
//
// job.Attempts distinguishes a fresh run from a retry of this same job — a
// real, persisted counter on the job's row (see queue/pgstore.go), carried
// through both Nack and staleness-reclaim redelivery, not reset per retry:
//   - Attempts == 0 (first dequeue): reverses this document's prior
//     canonical-entity contribution and wipes any entities from a previous,
//     unrelated extraction run (e.g. before a type-set change), same as
//     before this change — this is what makes re-running extraction on an
//     already-processed document idempotent.
//   - Attempts > 0 (retry of an interrupted attempt of this same job):
//     skips the wipe and resumes, sending only the chunks that don't
//     already have persisted entities from this job's earlier progress.
//
// A large document's extraction was measured taking long enough in one
// unbatched call to exceed JOB_STALE_TIMEOUT under normal operation, no
// deploy/restart involved — discarding all completed work on every retry.
// Batching bounds per-request duration; per-batch persistence plus this
// resume logic means a retry doesn't restart from zero.
func (h *EntityHandler) Handle(ctx context.Context, job *queue.Job) error {
	ctx = auth.WithUserID(ctx, job.UserID)

	// Confirm the document exists and belongs to this tenant before doing
	// any work; mirrors DocumentHandler's tenant-scoped lookup.
	//
	// Timed as a baseline for a possible cold-DB-connection hypothesis
	// (see the persistBatch timing below): this is the very first DB call
	// in Handle, before any AWS Batch waiting, so if it's already slow the
	// connection wasn't idle-suspended by ExtractBatches specifically --
	// something else would be going on.
	docLookupStart := time.Now()
	if _, err := h.docs.Get(ctx, job.UserID, job.DocumentID); err != nil {
		return fmt.Errorf("entityhandler: get document %s: %w", job.DocumentID, err)
	}
	log.Printf("entityhandler: job %s: initial docs.Get (baseline DB latency) took %s", job.ID, time.Since(docLookupStart))

	chunks, err := h.chunks.ListByDocument(ctx, job.UserID, job.DocumentID)
	if err != nil {
		return fmt.Errorf("entityhandler: list chunks for document %s: %w", job.DocumentID, err)
	}
	if len(chunks) == 0 {
		// No chunks yet (document indexing hasn't run, or produced none);
		// nothing to extract from. Not an error — chunking is a separate,
		// upstream job stage this handler does not trigger.
		return nil
	}

	if job.Attempts == 0 {
		// Reverse this document's current contribution to canonical entity
		// stats before its entity rows are replaced: canonical_entities
		// counts are cross-document state, so the delete-and-recreate
		// idempotency pattern below (safe for entities/edges, which are
		// wholly owned by one document) cannot apply to them directly — a
		// canonical row can be shared with other documents' mentions. This
		// must happen before the delete, since the mention -> canonical
		// linkage it reads is gone once the old rows are.
		if err := h.canonical.DecrementForDocument(ctx, job.UserID, job.DocumentID); err != nil {
			return fmt.Errorf("entityhandler: decrement canonical entities: %w", err)
		}
		// Delete any entities from a previous run before persisting the new
		// set, so re-running extraction (e.g. after a type-set change) is
		// idempotent and doesn't leave stale rows from the old type set
		// around. Only on the first attempt — a retry of this same job
		// must not wipe the partial progress it's about to resume from.
		if err := h.entities.DeleteByDocument(ctx, job.UserID, job.DocumentID); err != nil {
			return fmt.Errorf("entityhandler: clear existing entities: %w", err)
		}
	}

	alreadyDone, err := h.entities.ChunkIDsWithEntities(ctx, job.UserID, job.DocumentID)
	if err != nil {
		return fmt.Errorf("entityhandler: list chunks already extracted: %w", err)
	}
	remaining := make([]*chunk.Chunk, 0, len(chunks))
	for _, c := range chunks {
		if !alreadyDone[c.ID] {
			remaining = append(remaining, c)
		}
	}

	batchSize := h.batchSizeOrDefault()
	var batches [][]*chunk.Chunk
	for start := 0; start < len(remaining); start += batchSize {
		end := start + batchSize
		if end > len(remaining) {
			end = len(remaining)
		}
		batches = append(batches, remaining[start:end])
	}

	// entity.BatchExtractor is an optional capability -- feature-detected
	// here, not required by the Extractor interface -- so extractors that
	// don't support it (e.g. the in-memory test double) keep working via
	// the sequential, one-batch-at-a-time path exactly as before. See
	// entity.BatchExtractor's doc for why submitting every batch's job up
	// front matters specifically for the real AWS Batch extractor.
	if batchExtractor, ok := h.extractor.(entity.BatchExtractor); ok {
		// There's no natural "between batches" point to heartbeat at when
		// every batch is awaited concurrently in one call below, unlike
		// the sequential fallback -- run it on its own ticker instead,
		// for the same reason the sequential path heartbeats per batch:
		// protecting a long-running job from a false staleness reclaim.
		stopHeartbeat := h.startHeartbeatTicker(ctx, job)
		extractStart := time.Now()
		results := batchExtractor.ExtractBatches(ctx, batches, h.allowedTypes)
		log.Printf("entityhandler: job %s: ExtractBatches (all %d batches) returned after %s", job.ID, len(batches), time.Since(extractStart))
		stopHeartbeat()

		// Per-batch timing, with the first call broken out separately --
		// added to test a specific hypothesis for a measured ~2.6m gap
		// between AWS Batch's own work finishing and this job completing:
		// Neon's serverless Postgres compute suspending during the several
		// idle-on-the-DB-side minutes ExtractBatches just spent polling AWS
		// APIs, and needing to cold-wake for the first query back. If
		// that's real, the first persistBatch call here should show a
		// clear, isolated outlier against the rest.
		var errs []error
		for i, result := range results {
			persistStart := time.Now()
			var perr error
			if result.Err != nil {
				errs = append(errs, fmt.Errorf("entityhandler: extract: %w", result.Err))
			} else if err := h.persistBatch(ctx, job, result.Entities); err != nil {
				errs = append(errs, err)
				perr = err
			}
			label := "persistBatch"
			if i == 0 {
				label = "persistBatch (FIRST -- possible cold-DB-connection outlier)"
			}
			log.Printf("entityhandler: job %s: batch %d %s took %s (entities=%d, err=%v)", job.ID, i, label, time.Since(persistStart), len(result.Entities), perr)
		}
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
	} else {
		for _, batch := range batches {
			entities, err := h.extractor.Extract(ctx, batch, h.allowedTypes)
			if err != nil {
				return fmt.Errorf("entityhandler: extract: %w", err)
			}
			if err := h.persistBatch(ctx, job, entities); err != nil {
				return err
			}
			h.heartbeatOnce(ctx, job)
		}
	}

	// Queue the edge-extraction and canonicalization stages as distinct
	// jobs, inline in the ingestion flow behind the existing queue/job-stage
	// seam: each can be retried or re-run independently of (re-)extracting
	// entities and of each other. A publish failure here is logged, not
	// fatal — entity extraction has already succeeded and must not be
	// rolled back.
	if h.publisher != nil {
		publishStart := time.Now()
		if err := h.publisher.PublishEdgeExtraction(ctx, queue.EdgeExtractionRequested{
			DocumentID: job.DocumentID,
			UserID:     job.UserID,
		}); err != nil {
			log.Printf("entityhandler: failed to queue edge extraction for document %s: %v", job.DocumentID, err)
		}
		log.Printf("entityhandler: job %s: PublishEdgeExtraction took %s", job.ID, time.Since(publishStart))

		publishStart = time.Now()
		if err := h.publisher.PublishCanonicalization(ctx, queue.CanonicalizationRequested{
			DocumentID: job.DocumentID,
			UserID:     job.UserID,
		}); err != nil {
			log.Printf("entityhandler: failed to queue canonicalization for document %s: %v", job.DocumentID, err)
		}
		log.Printf("entityhandler: job %s: PublishCanonicalization took %s", job.ID, time.Since(publishStart))
	}

	return nil
}

// persistBatch backfills UserID (the Extractor is not required to
// populate it, so persisted rows are always correctly tenant-scoped
// regardless of which Extractor implementation ran) and persists
// entities. A no-op for an empty slice, so callers don't need to guard
// the common case of a batch that legitimately extracted nothing.
func (h *EntityHandler) persistBatch(ctx context.Context, job *queue.Job, entities []*entity.Entity) error {
	if len(entities) == 0 {
		return nil
	}
	for _, e := range entities {
		e.UserID = job.UserID
	}
	if err := h.entities.BulkCreate(ctx, entities); err != nil {
		return fmt.Errorf("entityhandler: persist entities: %w", err)
	}
	return nil
}

// heartbeatOnce sends a single heartbeat. A missed heartbeat risks a
// false staleness reclaim later, not data loss now — whatever's already
// persisted stays persisted — so this logs and keeps going rather than
// failing the whole job over an accounting write.
func (h *EntityHandler) heartbeatOnce(ctx context.Context, job *queue.Job) {
	if h.heartbeat == nil {
		return
	}
	if err := h.heartbeat.Heartbeat(ctx, job.ID); err != nil {
		log.Printf("entityhandler: heartbeat for job %s failed: %v", job.ID, err)
	}
}

// startHeartbeatTicker runs heartbeatOnce every heartbeatIntervalOrDefault
// until the returned stop function is called. Used around a single,
// possibly long BatchExtractor.ExtractBatches call, which — unlike the
// sequential fallback path — has no natural "between batches" point to
// heartbeat at, since every batch is awaited concurrently in one call.
// Callers must call the returned function exactly once, after
// ExtractBatches returns.
func (h *EntityHandler) startHeartbeatTicker(ctx context.Context, job *queue.Job) func() {
	if h.heartbeat == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(h.heartbeatIntervalOrDefault())
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				h.heartbeatOnce(ctx, job)
			}
		}
	}()
	return func() { close(done) }
}

// OnFailed logs that extraction was permanently dead-lettered for the
// document. It deliberately does not touch document.Status: entity
// extraction failures do not affect the document's chunking/embedding
// status, since this is an independent job stage. The document simply
// remains without entities until a new extraction job is queued.
func (h *EntityHandler) OnFailed(_ context.Context, job *queue.Job) {
	log.Printf("entityhandler: entity extraction permanently failed for document %s", job.DocumentID)
}

var _ Handler = (*EntityHandler)(nil)
