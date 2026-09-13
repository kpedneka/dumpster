package worker

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/llm"
	"github.com/kunalpednekar/dumpster/internal/manifest"
	"github.com/kunalpednekar/dumpster/internal/manifest/layout"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/stats"
)

// PhaseSetter is the narrow capability RegionClassificationHandler needs
// from the queue to record which sub-stage a job is in -- see
// WithPhaseTracking. queue.Consumer satisfies this.
type PhaseSetter interface {
	SetPhase(ctx context.Context, jobID uuid.UUID, phase string) error
}

// LayoutExtractor classifies PDF regions via scripts/extract_regions.py
// (pymupdf for native text, pdfplumber for native tables -- see that
// file's own docstring for why unstructured.io's figure/scanned-content
// layer was dropped entirely rather than kept). Defined here so
// RegionClassificationHandler depends on an interface rather than the
// concrete implementation directly; the real implementation
// (internal/manifest/awsbatch.Extractor) submits an AWS Batch job per
// call rather than an HTTP request.
type LayoutExtractor interface {
	ExtractRegions(ctx context.Context, pdfBytes []byte) ([]*layout.RawRegion, error)
}

// extractorVersion is stamped on every manifest row this handler produces.
// Bump when the classifier stack or VLM prompt changes significantly.
const extractorVersion = "v1"

// RegionClassificationHandler is the alternative ingestion entry point for
// PDF and image documents. It runs the layered classifier, produces the
// ingestion manifest, creates region-tagged chunks, embeds them, and marks
// the document as indexed — converging on the same end state as
// DocumentHandler for text/markdown files.
//
// Image uploads are handled as a degenerate case: one figure region covering
// the whole image, no Python subprocess needed.
//
// Regions the layered classifier can't resolve on its own (figures,
// suspected-scanned text/tables) are marked skipped rather than sent to a
// VLM for a follow-up call: the VLM follow-up step was never fully wired up
// (the image-cropping step that would give it real pixel data was stubbed
// out and never finished, so every such call was already describing/
// confirming nothing) and, independently, was measured costing 15-21s of
// stalled latency per region. Revisit as its own card if multimodal figure
// description becomes a real requirement.
//
// This handler is selected at upload time by the document upload handler
// based on content type; DocumentHandler remains the path for text/markdown.
type RegionClassificationHandler struct {
	docs      document.Repository
	objects   objectstore.ObjectStore
	chunks    chunk.Repository
	manifest  manifest.Repository
	layout    LayoutExtractor // nil for image uploads (bypassed)
	embedder  llm.Embedder
	publisher queue.Publisher
	// stats, when non-nil, records a successful index into the durable,
	// cross-tenant usage counters (see internal/stats). Optional, wired via
	// WithStats so existing call sites keep working unmodified.
	stats stats.Repository
	// phases, when non-nil, records the analyzing->embedding transition
	// partway through process() -- see WithPhaseTracking. Optional for
	// the same reason as stats: existing call sites (and tests) that don't
	// care about phase display keep working unmodified.
	phases PhaseSetter
}

// WithStats wires repo into h so that every document this handler
// successfully indexes is recorded into the durable, cross-tenant usage
// counters.
func (h *RegionClassificationHandler) WithStats(repo stats.Repository) *RegionClassificationHandler {
	h.stats = repo
	return h
}

// WithPhaseTracking wires ps into h so process() records the transition
// from region analysis to embedding -- the two sub-stages this job type
// runs back-to-back with no other jobs-table update in between, which is
// otherwise invisible to anything reading job status for display (see
// queue.StageFor and queue.PhaseEmbedding).
func (h *RegionClassificationHandler) WithPhaseTracking(ps PhaseSetter) *RegionClassificationHandler {
	h.phases = ps
	return h
}

// NewRegionClassificationHandler creates a handler. layoutExtractor may be
// nil for pure-image documents which don't need the Python sidecar.
func NewRegionClassificationHandler(
	docs document.Repository,
	objects objectstore.ObjectStore,
	chunks chunk.Repository,
	manifest manifest.Repository,
	layoutExtractor LayoutExtractor,
	embedder llm.Embedder,
	publisher queue.Publisher,
) *RegionClassificationHandler {
	return &RegionClassificationHandler{
		docs:      docs,
		objects:   objects,
		chunks:    chunks,
		manifest:  manifest,
		layout:    layoutExtractor,
		embedder:  embedder,
		publisher: publisher,
	}
}

// Handle classifies a PDF or image document's regions, produces the ingestion
// manifest, creates region-tagged chunks, embeds them, and marks the document
// as indexed. Converges on the same end state as DocumentHandler so that
// downstream stages (entity extraction, edge extraction) are unaware of which
// ingestion path was taken.
func (h *RegionClassificationHandler) Handle(ctx context.Context, job *queue.Job) error {
	ctx = auth.WithUserID(ctx, job.UserID)

	doc, err := h.docs.Get(ctx, job.UserID, job.DocumentID)
	if err != nil {
		return fmt.Errorf("regionhandler: get document %s: %w", job.DocumentID, err)
	}

	if doc.Status == document.StatusIndexed {
		return nil
	}

	if err := h.docs.UpdateStatus(ctx, job.UserID, job.DocumentID, document.StatusProcessing); err != nil {
		return fmt.Errorf("regionhandler: mark processing: %w", err)
	}

	processStart := time.Now()
	err = h.process(ctx, job.ID, doc)
	log.Printf("regionhandler: doc %s: process (regions+embed+persist) took %s (err=%v)", job.DocumentID, time.Since(processStart), err)
	if err != nil {
		return err
	}

	if err := h.docs.UpdateStatus(ctx, job.UserID, job.DocumentID, document.StatusIndexed); err != nil {
		return fmt.Errorf("regionhandler: mark indexed: %w", err)
	}

	if h.stats != nil {
		if err := h.stats.RecordDocumentIndexed(ctx, doc.SizeBytes); err != nil {
			log.Printf("regionhandler: failed to record usage stats for document %s: %v", job.DocumentID, err)
		}
	}

	// Entity extraction is published from inside process(), right after
	// chunks are persisted, not here -- see process()'s doc comment for
	// why. Publishing it again here would be at best redundant and at
	// worst a duplicate entity-extraction run: the jobs table only
	// deduplicates concurrent (document_id, job_type) pairs while one is
	// still pending/processing (see migrations/012_entity_extraction.sql),
	// so if process()'s early job already finished and was Acked by the
	// time Handle() reached this point, a second publish here would
	// enqueue a genuine second run.

	return nil
}

// process classifies regions, builds chunks, persists them, publishes
// entity extraction, embeds the chunks, and backfills their embeddings --
// in that order. Chunks are persisted (with a nil embedding) and entity
// extraction published *before* the embed call, not after: entity
// extraction only needs chunk text, not embedding vectors, so publishing
// it early lets a second worker goroutine dequeue and start extracting
// while this call is still blocked on the embed AWS Batch job, rather
// than only starting once this whole function returns.
func (h *RegionClassificationHandler) process(ctx context.Context, jobID uuid.UUID, doc *document.Document) error {
	readStart := time.Now()
	rawBytes, err := h.readObject(ctx, doc.S3Key)
	log.Printf("regionhandler: doc %s: readObject took %s (bytes=%d, err=%v)", doc.ID, time.Since(readStart), len(rawBytes), err)
	if err != nil {
		return err
	}

	var rawRegions []*layout.RawRegion
	if strings.HasPrefix(doc.ContentType, "image/") {
		// Image uploads are degenerate 1-region documents: the whole image
		// is a single figure region.
		rawRegions = []*layout.RawRegion{{
			RegionType:  "figure",
			PageNumber:  1,
			BoundingBox: [4]float64{0, 0, 1, 1},
			NeedsVLM:    "describe",
		}}
	} else {
		// PDF: run layered classifier (layers 1+2 via Python).
		if h.layout == nil {
			return fmt.Errorf("regionhandler: no layout extractor configured for PDF document %s", doc.ID)
		}
		extractStart := time.Now()
		rawRegions, err = h.layout.ExtractRegions(ctx, rawBytes)
		log.Printf("regionhandler: doc %s: ExtractRegions took %s (regions=%d, err=%v)", doc.ID, time.Since(extractStart), len(rawRegions), err)
		if err != nil {
			return fmt.Errorf("regionhandler: extract regions from %s: %w", doc.ID, err)
		}
	}

	// Regions the layered classifier couldn't resolve on its own (figures,
	// suspected-scanned text/tables) are marked skipped directly — no VLM
	// follow-up call (see the type doc for why).
	resolved := make([]resolvedRegion, 0, len(rawRegions))
	for _, r := range rawRegions {
		res := resolvedRegion{raw: r}
		switch r.NeedsVLM {
		case "describe":
			res.status = manifest.StatusSkipped
			res.regionType = manifest.RegionTypeFigure
		case "confirm_scan":
			res.status = manifest.StatusSkipped
			if r.RegionType == "unconfirmed_table" {
				res.regionType = manifest.RegionTypeScannedTable
			} else {
				res.regionType = manifest.RegionTypeScannedText
			}
		default:
			// Native text/table: already resolved by layers 1+2.
			res.text = r.Text
			if r.Text != "" {
				res.status = manifest.StatusIndexed
			} else {
				res.status = manifest.StatusSkipped
			}
			switch r.RegionType {
			case "native_table":
				res.regionType = manifest.RegionTypeNativeTable
			default:
				res.regionType = manifest.RegionTypeNativeText
			}
		}
		resolved = append(resolved, res)
	}

	// Delete any previous manifest rows from a prior attempt.
	deleteManifestStart := time.Now()
	if err := h.manifest.DeleteByDocument(ctx, doc.UserID, doc.ID); err != nil {
		return fmt.Errorf("regionhandler: clear existing manifest: %w", err)
	}
	log.Printf("regionhandler: doc %s: manifest.DeleteByDocument took %s", doc.ID, time.Since(deleteManifestStart))

	// Persist all regions, including skipped ones.
	regions := make([]*manifest.Region, 0, len(resolved))
	for _, res := range resolved {
		bb := res.raw.BoundingBox
		regions = append(regions, &manifest.Region{
			DocumentID:       doc.ID,
			KBID:             doc.KBID,
			UserID:           doc.UserID,
			RegionType:       res.regionType,
			PageNumber:       res.raw.PageNumber,
			BoundingBox:      manifest.BoundingBox{X0: bb[0], Y0: bb[1], X1: bb[2], Y1: bb[3]},
			Status:           res.status,
			ExtractorVersion: extractorVersion,
		})
	}
	bulkCreateManifestStart := time.Now()
	if err := h.manifest.BulkCreate(ctx, regions); err != nil {
		return fmt.Errorf("regionhandler: persist manifest: %w", err)
	}
	log.Printf("regionhandler: doc %s: manifest.BulkCreate took %s (rows=%d)", doc.ID, time.Since(bulkCreateManifestStart), len(regions))

	// Look up the newly-assigned region IDs.
	listManifestStart := time.Now()
	persistedRegions, err := h.manifest.ListByDocument(ctx, doc.UserID, doc.ID)
	if err != nil {
		return fmt.Errorf("regionhandler: list manifest after create: %w", err)
	}
	log.Printf("regionhandler: doc %s: manifest.ListByDocument took %s", doc.ID, time.Since(listManifestStart))

	// Build chunks for indexed regions.
	deleteChunksStart := time.Now()
	if err := h.chunks.DeleteByDocument(ctx, doc.UserID, doc.ID); err != nil {
		return fmt.Errorf("regionhandler: clear existing chunks: %w", err)
	}
	log.Printf("regionhandler: doc %s: chunks.DeleteByDocument took %s", doc.ID, time.Since(deleteChunksStart))

	splitStart := time.Now()
	var allChunks []*chunk.Chunk
	splitter := chunk.DefaultFixedWindow()
	ordinal := 0
	for i, res := range resolved {
		if res.status != manifest.StatusIndexed || res.text == "" {
			continue
		}
		var regionID *manifest.Region
		if i < len(persistedRegions) {
			regionID = persistedRegions[i]
		}
		page := res.raw.PageNumber
		bb := &chunk.BoundingBox{X0: res.raw.BoundingBox[0], Y0: res.raw.BoundingBox[1], X1: res.raw.BoundingBox[2], Y1: res.raw.BoundingBox[3]}
		splits := splitter.Split(res.text)
		for _, s := range splits {
			s.DocumentID = doc.ID
			s.KBID = doc.KBID
			s.UserID = doc.UserID
			s.Ordinal = ordinal
			s.PageNumber = &page
			s.BoundingBox = bb
			if regionID != nil {
				rid := regionID.ID
				s.RegionID = &rid
			}
			ordinal++
			allChunks = append(allChunks, s)
		}
	}

	log.Printf("regionhandler: doc %s: build chunks from regions took %s (chunks=%d)", doc.ID, time.Since(splitStart), len(allChunks))

	if len(allChunks) == 0 {
		h.publishEntityExtraction(ctx, doc)
		return nil
	}

	// Persist chunks now, with Embedding left nil, rather than waiting
	// until after the embed call below returns. A nil embedding is
	// already a normal state chunks support (see migrations/
	// 019_local_embeddings.sql: such a chunk simply drops out of vector
	// search, keyword search still covers it) -- what matters here is
	// that entity extraction (a distinct job/process reading chunk rows
	// straight from Postgres, never from this function's local memory)
	// has real rows to read before this function ever calls Embed, so
	// publishEntityExtraction below can run concurrently with the embed
	// call instead of waiting for it.
	persistChunksStart := time.Now()
	if err := h.chunks.BulkCreate(ctx, allChunks); err != nil {
		return fmt.Errorf("regionhandler: persist chunks: %w", err)
	}
	log.Printf("regionhandler: doc %s: chunks.BulkCreate (text only) took %s (rows=%d)", doc.ID, time.Since(persistChunksStart), len(allChunks))

	h.publishEntityExtraction(ctx, doc)

	if h.phases != nil {
		if err := h.phases.SetPhase(ctx, jobID, queue.PhaseEmbedding); err != nil {
			// Best-effort: this only affects what the upload-progress UI
			// displays, not correctness of the pipeline itself -- not
			// worth failing an otherwise-successful job over.
			log.Printf("regionhandler: doc %s: failed to set phase to embedding: %v", doc.ID, err)
		}
	}

	texts := make([]string, len(allChunks))
	for i, c := range allChunks {
		texts[i] = c.Text
	}
	embedStart := time.Now()
	vecs, err := h.embedder.Embed(ctx, texts)
	log.Printf("regionhandler: doc %s: embedder.Embed took %s (texts=%d, err=%v)", doc.ID, time.Since(embedStart), len(texts), err)
	if err != nil {
		return fmt.Errorf("regionhandler: embed: %w", err)
	}
	if len(vecs) != len(allChunks) {
		return fmt.Errorf("regionhandler: embedder returned %d vectors for %d chunks", len(vecs), len(allChunks))
	}

	// Backfill each chunk's embedding by re-listing (ordered by ordinal,
	// matching the order allChunks/texts/vecs were built in) rather than
	// using allChunks directly -- BulkCreate never assigns generated IDs
	// back onto its input slice, so this is the only way to learn which
	// database row each vector belongs to. Same insert-then-backfill
	// pattern internal/reembed/reembed.go already uses.
	backfillStart := time.Now()
	persisted, err := h.chunks.ListByDocument(ctx, doc.UserID, doc.ID)
	if err != nil {
		return fmt.Errorf("regionhandler: list chunks for embedding backfill: %w", err)
	}
	if len(persisted) != len(vecs) {
		return fmt.Errorf("regionhandler: chunk/embedding count mismatch: %d persisted chunks, %d vectors", len(persisted), len(vecs))
	}
	for i, c := range persisted {
		if err := h.chunks.UpdateEmbedding(ctx, doc.UserID, c.ID, vecs[i]); err != nil {
			return fmt.Errorf("regionhandler: update embedding for chunk %s: %w", c.ID, err)
		}
	}
	log.Printf("regionhandler: doc %s: backfill embeddings took %s (rows=%d)", doc.ID, time.Since(backfillStart), len(persisted))

	return nil
}

// publishEntityExtraction enqueues the entity-extraction stage for doc.
// Called from inside process() -- right after chunks are persisted,
// whether or not there are any to embed -- rather than from Handle()
// after the document reaches Indexed, so entity extraction can run
// concurrently with the embed call above instead of waiting for it.
func (h *RegionClassificationHandler) publishEntityExtraction(ctx context.Context, doc *document.Document) {
	if h.publisher == nil {
		return
	}
	if err := h.publisher.PublishEntityExtraction(ctx, queue.EntityExtractionRequested{
		DocumentID: doc.ID,
		UserID:     doc.UserID,
	}); err != nil {
		log.Printf("regionhandler: failed to queue entity extraction for document %s: %v", doc.ID, err)
	}
}

type resolvedRegion struct {
	raw        *layout.RawRegion
	regionType manifest.RegionType
	status     manifest.Status
	text       string
}

func (h *RegionClassificationHandler) readObject(ctx context.Context, key string) ([]byte, error) {
	rc, err := h.objects.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("regionhandler: read object %s: %w", key, err)
	}
	defer func() {
		if cerr := rc.Close(); cerr != nil {
			log.Printf("regionhandler: close object %s: %v", key, cerr)
		}
	}()
	return io.ReadAll(rc)
}

// OnFailed marks the document as failed when the job is dead-lettered.
// Mirrors DocumentHandler.OnFailed so the document lifecycle is consistent
// regardless of which ingestion path was taken.
func (h *RegionClassificationHandler) OnFailed(ctx context.Context, job *queue.Job) {
	ctx = auth.WithUserID(ctx, job.UserID)
	if err := h.docs.UpdateStatus(ctx, job.UserID, job.DocumentID, document.StatusFailed); err != nil {
		log.Printf("regionhandler: failed to mark document %s as failed: %v", job.DocumentID, err)
	}
}

var _ Handler = (*RegionClassificationHandler)(nil)
