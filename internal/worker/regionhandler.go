package worker

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/llm"
	"github.com/kunalpednekar/dumpster/internal/manifest"
	"github.com/kunalpednekar/dumpster/internal/manifest/layout"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// LayoutExtractor classifies PDF regions via the Python layered classifier
// (layers 1+2: pdfplumber + unstructured.io). Defined here so the
// RegionClassificationHandler depends on an interface rather than the
// concrete layout.Extractor directly.
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

	if err := h.process(ctx, doc); err != nil {
		return err
	}

	if err := h.docs.UpdateStatus(ctx, job.UserID, job.DocumentID, document.StatusIndexed); err != nil {
		return fmt.Errorf("regionhandler: mark indexed: %w", err)
	}

	if h.publisher != nil {
		if err := h.publisher.PublishEntityExtraction(ctx, queue.EntityExtractionRequested{
			DocumentID: job.DocumentID,
			UserID:     job.UserID,
		}); err != nil {
			log.Printf("regionhandler: failed to queue entity extraction for document %s: %v", job.DocumentID, err)
		}
	}

	return nil
}

func (h *RegionClassificationHandler) process(ctx context.Context, doc *document.Document) error {
	rawBytes, err := h.readObject(ctx, doc.S3Key)
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
		rawRegions, err = h.layout.ExtractRegions(ctx, rawBytes)
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
	if err := h.manifest.DeleteByDocument(ctx, doc.UserID, doc.ID); err != nil {
		return fmt.Errorf("regionhandler: clear existing manifest: %w", err)
	}

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
	if err := h.manifest.BulkCreate(ctx, regions); err != nil {
		return fmt.Errorf("regionhandler: persist manifest: %w", err)
	}

	// Look up the newly-assigned region IDs.
	persistedRegions, err := h.manifest.ListByDocument(ctx, doc.UserID, doc.ID)
	if err != nil {
		return fmt.Errorf("regionhandler: list manifest after create: %w", err)
	}

	// Build chunks for indexed regions.
	if err := h.chunks.DeleteByDocument(ctx, doc.UserID, doc.ID); err != nil {
		return fmt.Errorf("regionhandler: clear existing chunks: %w", err)
	}

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

	if len(allChunks) == 0 {
		return nil
	}

	texts := make([]string, len(allChunks))
	for i, c := range allChunks {
		texts[i] = c.Text
	}
	vecs, err := h.embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("regionhandler: embed: %w", err)
	}
	for i, c := range allChunks {
		c.Embedding = vecs[i]
	}
	if err := h.chunks.BulkCreate(ctx, allChunks); err != nil {
		return fmt.Errorf("regionhandler: persist chunks: %w", err)
	}

	return nil
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
