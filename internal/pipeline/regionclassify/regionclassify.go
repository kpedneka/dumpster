// Package regionclassify implements the Lambda steps of the region
// classification state machine (statemachine.asl.json in this package),
// which replaces the worker's RegionClassificationHandler for PDF and
// image uploads:
//
//  1. StageLayout: mark the attempt processing (the progress checklist's
//     "Analyzing" stage) and, for a PDF, presign the original upload and a
//     result URL for the layout Batch job. Images skip that job.
//  2. Layout: the state machine runs the layout job itself
//     (batch:submitJob.sync).
//  3. BuildChunks: read the layout result from S3 (region text can exceed
//     Step Functions' 256KB state limit, so it never passes through the
//     execution state), replace the document's manifest and chunks,
//     publish entity extraction, switch the phase to embedding, and stage
//     the embedding job.
//  4. Embed, Finalize, RecordFailure: the same embedding tail as document
//     indexing (internal/pipeline/embedtail), plus deleting the layout
//     result.
//
// Regions the layered classifier can't resolve on its own (figures,
// suspected scans) are recorded as skipped, as in the worker; there's no
// VLM follow-up call.
package regionclassify

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/manifest"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/pipeline/embedtail"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/stats"
)

// The steps the state machine's Lambda tasks send in Event.Step.
const (
	StepStageLayout   = "stage_layout"
	StepBuildChunks   = "build_chunks"
	StepFinalize      = "finalize"
	StepRecordFailure = "record_failure"
)

// extractorVersion is recorded on every manifest region. Bump when the
// classifier stack changes significantly. Matches the worker's value.
const extractorVersion = "v1"

// Definition is the state machine definition, in Amazon States Language,
// with ${name} placeholders for the values listed in DefinitionVars.
// Terraform fills them with templatefile() on this same file.
//
//go:embed statemachine.asl.json
var Definition string

// DefinitionVars are the placeholders in Definition: the Lambda running
// this package's Handler, and the Batch queues and job definitions for the
// layout and embedding jobs.
var DefinitionVars = []string{"lambda_arn", "layout_job_queue", "layout_job_definition", "embed_job_queue", "embed_job_definition"}

// RenderDefinition returns Definition with each ${name} placeholder
// replaced by vars[name].
func RenderDefinition(vars map[string]string) string {
	pairs := make([]string, 0, 2*len(vars))
	for name, value := range vars {
		pairs = append(pairs, "${"+name+"}", value)
	}
	return strings.NewReplacer(pairs...).Replace(Definition)
}

// Event is the payload every Lambda task sends: the step to run, and the
// whole execution state as Run.
type Event struct {
	Step string `json:"step"`
	Run  Input  `json:"run"`
}

// Input is the execution state. It starts as the execution input
// internal/queue/stepfunctions sends ({type, document_id, user_id,
// attempt}). StageLayout's result is added at $.layout, BuildChunks' at
// $.prepare, and a caught error at $.error.
type Input struct {
	Type       queue.JobType       `json:"type"`
	DocumentID uuid.UUID           `json:"document_id"`
	UserID     uuid.UUID           `json:"user_id"`
	Attempt    int                 `json:"attempt"`
	Layout     *Layout             `json:"layout,omitempty"`
	Prepare    *Prepared           `json:"prepare,omitempty"`
	Error      *pipeline.StepError `json:"error,omitempty"`
}

// Layout is StageLayout's result. The state machine reads NeedsLayout to
// decide whether to run the layout job, and passes JobName, PDFURL and
// ResultURL to it.
type Layout struct {
	// Stale means this attempt is no longer current, so later steps do
	// nothing.
	Stale bool `json:"stale,omitempty"`
	// AlreadyIndexed means the document was indexed before this run.
	AlreadyIndexed bool   `json:"already_indexed,omitempty"`
	NeedsLayout    bool   `json:"needs_layout"`
	JobName        string `json:"job_name,omitempty"`
	// PDFURL is a presigned GET URL for the original upload.
	PDFURL    string `json:"pdf_url,omitempty"`
	ResultURL string `json:"result_url,omitempty"`
	// ResultKey is the scratch object the layout job writes to.
	ResultKey string `json:"result_key,omitempty"`
}

// Prepared is BuildChunks' result, the same shape as document indexing's
// Prepare result. The state machine reads Embed to decide whether to run
// the embedding job.
type Prepared struct {
	Stale          bool   `json:"stale,omitempty"`
	AlreadyIndexed bool   `json:"already_indexed,omitempty"`
	Embed          bool   `json:"embed"`
	ChunkCount     int    `json:"chunk_count,omitempty"`
	JobName        string `json:"job_name,omitempty"`
	TextsURL       string `json:"texts_url,omitempty"`
	ResultURL      string `json:"result_url,omitempty"`
	InputKey       string `json:"input_key,omitempty"`
	ResultKey      string `json:"result_key,omitempty"`
}

// Deps are the Handler's collaborators. Stats is optional.
type Deps struct {
	Docs document.Repository
	// Uploads is the store user documents live in; the layout job reads
	// the PDF from it through a presigned URL.
	Uploads objectstore.ObjectStore
	// Scratch holds the layout and embedding jobs' handoff objects.
	Scratch  objectstore.ObjectStore
	Chunks   chunk.Repository
	Manifest manifest.Repository
	Splitter chunk.Splitter
	// Publisher publishes entity extraction once chunks exist.
	Publisher queue.Publisher
	Status    jobstatus.Writer
	Stats     stats.Repository
}

// Config holds the Handler's tunables.
type Config struct {
	// PresignTTL is how long the Batch jobs' URLs stay valid, covering
	// their queue wait. Defaults to 1h.
	PresignTTL time.Duration
}

// Handler runs the region classification state machine's Lambda steps.
type Handler struct {
	deps       Deps
	presignTTL time.Duration
	tail       *embedtail.Tail
}

// New returns a Handler.
func New(deps Deps, cfg Config) *Handler {
	if cfg.PresignTTL <= 0 {
		cfg.PresignTTL = time.Hour
	}
	return &Handler{deps: deps, presignTTL: cfg.PresignTTL, tail: embedtail.New(embedtail.Deps{
		Docs: deps.Docs, Chunks: deps.Chunks, Scratch: deps.Scratch, Status: deps.Status, Stats: deps.Stats,
	}, cfg.PresignTTL)}
}

// Handle routes ev to its step. It's the Lambda handler's body: errors
// come back through pipeline.ForLambda, so transient ones reach the state
// machine as TransientError.
func (h *Handler) Handle(ctx context.Context, ev Event) (any, error) {
	var out any
	var err error
	switch ev.Step {
	case StepStageLayout:
		out, err = h.StageLayout(ctx, ev.Run)
	case StepBuildChunks:
		out, err = h.BuildChunks(ctx, ev.Run)
	case StepFinalize:
		err = h.Finalize(ctx, ev.Run)
	case StepRecordFailure:
		err = h.RecordFailure(ctx, ev.Run)
	default:
		err = fmt.Errorf("regionclassify: unknown step %q", ev.Step)
	}
	return out, pipeline.ForLambda(err)
}

func (in Input) run() embedtail.Run {
	return embedtail.Run{JobType: queue.JobTypeRegionClassification, DocumentID: in.DocumentID, UserID: in.UserID, Attempt: in.Attempt}
}

// StageLayout marks the attempt processing and, for a PDF, presigns the
// original upload and a result URL for the layout job.
func (h *Handler) StageLayout(ctx context.Context, in Input) (Layout, error) {
	ctx = auth.WithUserID(ctx, in.UserID)

	if err := h.deps.Status.MarkProcessing(ctx, in.run().Key(), in.Attempt); err != nil {
		if pipeline.Superseded(err) {
			return Layout{Stale: true}, nil
		}
		return Layout{}, fmt.Errorf("regionclassify: stage layout: mark processing: %w", pipeline.TransientUnless(err, jobstatus.ErrNotFound))
	}
	doc, err := h.deps.Docs.Get(ctx, in.UserID, in.DocumentID)
	if err != nil {
		return Layout{}, fmt.Errorf("regionclassify: stage layout: get document %s: %w", in.DocumentID, pipeline.TransientUnless(err, document.ErrNotFound))
	}
	if doc.Status == document.StatusIndexed {
		return Layout{AlreadyIndexed: true}, nil
	}
	if err := h.deps.Docs.UpdateStatus(ctx, in.UserID, in.DocumentID, document.StatusProcessing); err != nil {
		return Layout{}, fmt.Errorf("regionclassify: stage layout: mark document processing: %w", pipeline.Transient(err))
	}
	if isImage(doc) {
		return Layout{}, nil
	}

	l := Layout{
		NeedsLayout: true,
		JobName:     fmt.Sprintf("region-extraction-%s-%d", in.DocumentID, in.Attempt),
		ResultKey:   fmt.Sprintf("batch-jobs/regions/%s-%d/result.json", in.DocumentID, in.Attempt),
	}
	if l.PDFURL, err = h.deps.Uploads.PresignedURL(ctx, doc.S3Key, h.presignTTL); err != nil {
		return Layout{}, fmt.Errorf("regionclassify: stage layout: presign upload: %w", pipeline.Transient(err))
	}
	if l.ResultURL, err = h.deps.Scratch.PresignedPutURL(ctx, l.ResultKey, h.presignTTL); err != nil {
		return Layout{}, fmt.Errorf("regionclassify: stage layout: presign layout result: %w", pipeline.Transient(err))
	}
	return l, nil
}

// isImage reports whether doc is an image upload, which is indexed as a
// single figure region with no layout job.
func isImage(doc *document.Document) bool {
	return strings.HasPrefix(doc.ContentType, "image/")
}

// rawRegion matches scripts/batch_regions_job.py's region JSON, the same
// shape internal/manifest/awsbatch reads.
type rawRegion struct {
	RegionType  string     `json:"region_type"`
	PageNumber  int        `json:"page_number"`
	BoundingBox [4]float64 `json:"bbox"`
	Text        string     `json:"text"`
	NeedsVLM    string     `json:"needs_vlm"`
}

// resolvedRegion is a raw region with its manifest type and status
// decided.
type resolvedRegion struct {
	raw        rawRegion
	regionType manifest.RegionType
	status     manifest.Status
}

// BuildChunks turns the layout result into manifest regions and chunks,
// publishes entity extraction, switches the phase to embedding, and
// stages the embedding job. It replaces any manifest and chunks a
// previous run left, so it's safe to repeat.
func (h *Handler) BuildChunks(ctx context.Context, in Input) (Prepared, error) {
	ctx = auth.WithUserID(ctx, in.UserID)
	l := in.Layout
	if l == nil {
		return Prepared{}, errors.New("regionclassify: build chunks: missing layout result")
	}
	if l.Stale {
		return Prepared{Stale: true}, nil
	}
	if l.AlreadyIndexed {
		return Prepared{AlreadyIndexed: true}, nil
	}

	// MarkProcessing doubles as the "is this attempt still current?"
	// check, so a superseded attempt writes nothing.
	if err := h.deps.Status.MarkProcessing(ctx, in.run().Key(), in.Attempt); err != nil {
		if pipeline.Superseded(err) {
			return Prepared{Stale: true}, nil
		}
		return Prepared{}, fmt.Errorf("regionclassify: build chunks: check attempt: %w", pipeline.TransientUnless(err, jobstatus.ErrNotFound))
	}
	doc, err := h.deps.Docs.Get(ctx, in.UserID, in.DocumentID)
	if err != nil {
		return Prepared{}, fmt.Errorf("regionclassify: build chunks: get document %s: %w", in.DocumentID, pipeline.TransientUnless(err, document.ErrNotFound))
	}

	raws := []rawRegion{{RegionType: "figure", PageNumber: 1, BoundingBox: [4]float64{0, 0, 1, 1}, NeedsVLM: "describe"}}
	if l.NeedsLayout {
		if raws, err = h.readLayoutResult(ctx, l.ResultKey); err != nil {
			return Prepared{}, fmt.Errorf("regionclassify: build chunks: %w", err)
		}
	}
	resolved := resolve(raws)

	persisted, err := h.replaceManifest(ctx, doc, resolved)
	if err != nil {
		return Prepared{}, fmt.Errorf("regionclassify: build chunks: %w", err)
	}
	chunks := h.buildChunks(doc, resolved, persisted)

	if err := h.deps.Chunks.DeleteByDocument(ctx, in.UserID, in.DocumentID); err != nil {
		return Prepared{}, fmt.Errorf("regionclassify: build chunks: clear existing chunks: %w", pipeline.Transient(err))
	}
	if len(chunks) == 0 {
		h.publishEntityExtraction(ctx, doc)
		return Prepared{}, nil
	}
	if err := h.deps.Chunks.BulkCreate(ctx, chunks); err != nil {
		return Prepared{}, fmt.Errorf("regionclassify: build chunks: persist chunks: %w", pipeline.Transient(err))
	}
	h.publishEntityExtraction(ctx, doc)

	if err := h.deps.Status.SetPhase(ctx, in.run().Key(), in.Attempt, queue.PhaseEmbedding); err != nil && !pipeline.Superseded(err) {
		return Prepared{}, fmt.Errorf("regionclassify: build chunks: set phase: %w", pipeline.TransientUnless(err, jobstatus.ErrNotFound))
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	st, err := h.tail.Stage(ctx, in.run(), texts)
	if err != nil {
		return Prepared{}, fmt.Errorf("regionclassify: build chunks: %w", err)
	}
	return Prepared{
		Embed: true, ChunkCount: st.ChunkCount, JobName: st.JobName, TextsURL: st.TextsURL,
		ResultURL: st.ResultURL, InputKey: st.InputKey, ResultKey: st.ResultKey,
	}, nil
}

func (h *Handler) readLayoutResult(ctx context.Context, key string) ([]rawRegion, error) {
	rc, err := h.deps.Scratch.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("read layout result %s: %w", key, pipeline.Transient(err))
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read layout result %s: %w", key, pipeline.Transient(err))
	}
	var resp struct {
		Regions []rawRegion `json:"regions"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode layout result %s: %w", key, err)
	}
	return resp.Regions, nil
}

// resolve decides each region's manifest type and status, as the worker
// does: native text and tables with text are indexed; figures, suspected
// scans and empty regions are skipped.
func resolve(raws []rawRegion) []resolvedRegion {
	out := make([]resolvedRegion, len(raws))
	for i, r := range raws {
		res := resolvedRegion{raw: r, status: manifest.StatusSkipped}
		switch r.NeedsVLM {
		case "describe":
			res.regionType = manifest.RegionTypeFigure
		case "confirm_scan":
			res.regionType = manifest.RegionTypeScannedText
			if r.RegionType == "unconfirmed_table" {
				res.regionType = manifest.RegionTypeScannedTable
			}
		default:
			res.regionType = manifest.RegionTypeNativeText
			if r.RegionType == "native_table" {
				res.regionType = manifest.RegionTypeNativeTable
			}
			if r.Text != "" {
				res.status = manifest.StatusIndexed
			}
		}
		out[i] = res
	}
	return out
}

// replaceManifest replaces doc's manifest with one region per resolved
// region (skipped ones included) and returns them as persisted, with IDs.
func (h *Handler) replaceManifest(ctx context.Context, doc *document.Document, resolved []resolvedRegion) ([]*manifest.Region, error) {
	if err := h.deps.Manifest.DeleteByDocument(ctx, doc.UserID, doc.ID); err != nil {
		return nil, fmt.Errorf("clear existing manifest: %w", pipeline.Transient(err))
	}
	regions := make([]*manifest.Region, len(resolved))
	for i, res := range resolved {
		bb := res.raw.BoundingBox
		regions[i] = &manifest.Region{
			DocumentID: doc.ID, KBID: doc.KBID, UserID: doc.UserID,
			RegionType: res.regionType, PageNumber: res.raw.PageNumber,
			BoundingBox:      manifest.BoundingBox{X0: bb[0], Y0: bb[1], X1: bb[2], Y1: bb[3]},
			Status:           res.status,
			ExtractorVersion: extractorVersion,
		}
	}
	if err := h.deps.Manifest.BulkCreate(ctx, regions); err != nil {
		return nil, fmt.Errorf("persist manifest: %w", pipeline.Transient(err))
	}
	persisted, err := h.deps.Manifest.ListByDocument(ctx, doc.UserID, doc.ID)
	if err != nil {
		return nil, fmt.Errorf("list manifest: %w", pipeline.Transient(err))
	}
	return persisted, nil
}

// buildChunks splits each indexed region's text into chunks tagged with
// the region, page and bounding box, numbered in order across the
// document. persisted is the manifest in the same order as resolved (both
// page order, top to bottom), which is how each chunk finds its region's
// ID, as in the worker.
func (h *Handler) buildChunks(doc *document.Document, resolved []resolvedRegion, persisted []*manifest.Region) []*chunk.Chunk {
	var out []*chunk.Chunk
	for i, res := range resolved {
		if res.status != manifest.StatusIndexed {
			continue
		}
		page := res.raw.PageNumber
		bb := res.raw.BoundingBox
		box := &chunk.BoundingBox{X0: bb[0], Y0: bb[1], X1: bb[2], Y1: bb[3]}
		for _, c := range h.deps.Splitter.Split(res.raw.Text) {
			c.DocumentID, c.KBID, c.UserID = doc.ID, doc.KBID, doc.UserID
			c.Ordinal = len(out)
			c.PageNumber, c.BoundingBox = &page, box
			if i < len(persisted) {
				id := persisted[i].ID
				c.RegionID = &id
			}
			out = append(out, c)
		}
	}
	return out
}

// publishEntityExtraction queues entity extraction for doc. A failure is
// logged, not fatal, as in the worker: indexing can still succeed, and
// entity extraction can be re-run on its own.
func (h *Handler) publishEntityExtraction(ctx context.Context, doc *document.Document) {
	if err := h.deps.Publisher.PublishEntityExtraction(ctx, queue.EntityExtractionRequested{
		DocumentID: doc.ID, UserID: doc.UserID,
	}); err != nil {
		log.Printf("regionclassify: failed to queue entity extraction for document %s: %v", doc.ID, err)
	}
}

// staged returns p's embedding job objects, or nil if nothing was staged.
func (p *Prepared) staged() *embedtail.Staged {
	if p == nil || !p.Embed {
		return nil
	}
	return &embedtail.Staged{
		ChunkCount: p.ChunkCount, JobName: p.JobName, TextsURL: p.TextsURL,
		ResultURL: p.ResultURL, InputKey: p.InputKey, ResultKey: p.ResultKey,
	}
}

// layoutResultKey returns the layout result object to clean up, if any.
func (in Input) layoutResultKey() string {
	if in.Layout == nil {
		return ""
	}
	return in.Layout.ResultKey
}

// Finalize backfills the chunks' embeddings (if any), marks the document
// indexed and the job succeeded, and deletes the Batch handoff objects.
func (h *Handler) Finalize(ctx context.Context, in Input) error {
	ctx = auth.WithUserID(ctx, in.UserID)
	p := in.Prepare
	if p == nil {
		return errors.New("regionclassify: finalize: missing build_chunks result")
	}
	if p.Stale {
		return nil
	}
	if err := h.tail.Finalize(ctx, in.run(), p.staged(), p.AlreadyIndexed, in.layoutResultKey()); err != nil {
		return fmt.Errorf("regionclassify: finalize: %w", err)
	}
	return nil
}

// RecordFailure marks the attempt and the document failed and deletes the
// Batch handoff objects. If the attempt was superseded by a retry, the
// document belongs to the newer attempt and is left alone.
func (h *Handler) RecordFailure(ctx context.Context, in Input) error {
	ctx = auth.WithUserID(ctx, in.UserID)
	if err := h.tail.RecordFailure(ctx, in.run(), in.Error, in.Prepare.staged(), in.layoutResultKey()); err != nil {
		return fmt.Errorf("regionclassify: record failure: %w", err)
	}
	return nil
}
