// Package entityextract implements the Lambda steps of the entity
// extraction state machine (statemachine.asl.json in this package), which
// replaces the worker's EntityHandler:
//
//  1. Plan: clear the document's entities from any previous run (reversing
//     their canonical-entity counts first), split its chunks into batches,
//     and stage each batch's input in S3. Only the small list of S3 keys
//     goes into the execution state, never chunk text.
//  2. ExtractBatches, an inline Map with one iteration per batch:
//     PresignBatch presigns that batch's input and result URLs, the state
//     machine runs the GPU Batch job itself (batch:submitJob.sync), and
//     PersistBatch saves that batch's entities as soon as it finishes.
//  3. Finalize: queue edge extraction and canonicalization, and mark the
//     job succeeded.
//  4. RecordFailure: any failure lands here and marks the attempt failed.
//     Entity extraction never changes the document's own status.
//
// There is no resume-from-partial-progress or heartbeat logic, unlike the
// worker. Both existed to survive the Postgres queue reclaiming a slow job
// mid-run, which can't happen under Step Functions. A retry is a new
// attempt that starts over from Plan.
//
// Known gap, carried over from the worker: if some batches persist and a
// later one fails, the persisted entities stay until the next attempt's
// Plan clears them.
package entityextract

import (
	"bytes"
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
	"github.com/kunalpednekar/dumpster/internal/canonical"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/entity"
	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// The steps the state machine's Lambda tasks send in Event.Step.
const (
	StepPlan          = "plan"
	StepPresignBatch  = "presign_batch"
	StepPersistBatch  = "persist_batch"
	StepFinalize      = "finalize"
	StepRecordFailure = "record_failure"
)

// Definition is the state machine definition, in Amazon States Language,
// with ${name} placeholders for the values listed in DefinitionVars.
// Terraform fills them with templatefile() on this same file.
//
//go:embed statemachine.asl.json
var Definition string

// DefinitionVars are the placeholders in Definition: the Lambda running
// this package's Handler, and the GPU Batch queue and job definition for
// entity extraction.
var DefinitionVars = []string{"lambda_arn", "entity_job_queue", "entity_job_definition"}

// RenderDefinition returns Definition with each ${name} placeholder
// replaced by vars[name].
func RenderDefinition(vars map[string]string) string {
	pairs := make([]string, 0, 2*len(vars))
	for name, value := range vars {
		pairs = append(pairs, "${"+name+"}", value)
	}
	return strings.NewReplacer(pairs...).Replace(Definition)
}

// Event is the payload every Lambda task sends. Top-level steps send the
// whole execution state as Run; the Map's steps send the run fields and
// their Batch.
type Event struct {
	Step  string `json:"step"`
	Run   Input  `json:"run"`
	Batch *Batch `json:"batch,omitempty"`
}

// Input is the execution state. It starts as the execution input
// internal/queue/stepfunctions sends ({type, document_id, user_id,
// attempt}). Plan's result is added at $.plan, and a caught error at
// $.error.
type Input struct {
	Type       queue.JobType       `json:"type"`
	DocumentID uuid.UUID           `json:"document_id"`
	UserID     uuid.UUID           `json:"user_id"`
	Attempt    int                 `json:"attempt"`
	Plan       *Plan               `json:"plan,omitempty"`
	Error      *pipeline.StepError `json:"error,omitempty"`
}

// Plan is the plan step's result. Batches is always a JSON array, empty
// when there's nothing to extract, because the Map state needs one.
type Plan struct {
	// Stale means this attempt is no longer current, so later steps do
	// nothing.
	Stale bool `json:"stale,omitempty"`
	// NoChunks means the document has no chunks yet, so there's nothing
	// to extract and nothing to queue downstream.
	NoChunks bool    `json:"no_chunks,omitempty"`
	Batches  []Batch `json:"batches"`
}

// Batch is one Map iteration's item: the S3 objects for one batch of
// chunks.
type Batch struct {
	Index     int    `json:"index"`
	InputKey  string `json:"input_key"`
	ResultKey string `json:"result_key"`
}

// BatchURLs is the presign_batch step's result, which the Batch job task
// reads.
type BatchURLs struct {
	JobName   string `json:"job_name"`
	ChunksURL string `json:"chunks_url"`
	ResultURL string `json:"result_url"`
}

// Deps are the Handler's collaborators.
type Deps struct {
	Docs      document.Repository
	Chunks    chunk.Repository
	Entities  entity.Repository
	Canonical canonical.Repository
	Objects   objectstore.ObjectStore
	// Publisher queues edge extraction and canonicalization.
	Publisher queue.Publisher
	Status    jobstatus.Writer
}

// Config holds the Handler's tunables.
type Config struct {
	// AllowedTypes is the configured entity type set (ENTITY_TYPES).
	AllowedTypes []string
	// BatchSize is how many chunks go to one Batch job. Defaults to 50.
	BatchSize int
	// PresignTTL is how long a batch's URLs stay valid, covering its wait
	// in the GPU Batch queue. Defaults to 2h.
	PresignTTL time.Duration
}

// Handler runs the entity extraction state machine's Lambda steps.
type Handler struct {
	deps Deps
	cfg  Config
}

// New returns a Handler.
func New(deps Deps, cfg Config) *Handler {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	if cfg.PresignTTL <= 0 {
		cfg.PresignTTL = 2 * time.Hour
	}
	return &Handler{deps: deps, cfg: cfg}
}

// entitiesRequest and entitiesResponse match the entity extraction Batch
// job's protocol, the same one internal/entity/awsbatch uses.
type entitiesRequest struct {
	AllowedTypes []string       `json:"allowed_types"`
	Chunks       []chunkRequest `json:"chunks"`
}

type chunkRequest struct {
	ChunkID string `json:"chunk_id"`
	Text    string `json:"text"`
}

type entitiesResponse struct {
	Entities []struct {
		ChunkID string  `json:"chunk_id"`
		Type    string  `json:"type"`
		Text    string  `json:"text"`
		Start   int     `json:"start"`
		End     int     `json:"end"`
		Score   float32 `json:"score"`
	} `json:"entities"`
}

// Handle routes ev to its step. It's the Lambda handler's body: errors
// come back through pipeline.ForLambda, so transient ones reach the state
// machine as TransientError.
func (h *Handler) Handle(ctx context.Context, ev Event) (any, error) {
	var out any
	var err error
	switch ev.Step {
	case StepPlan:
		out, err = h.Plan(ctx, ev.Run)
	case StepPresignBatch, StepPersistBatch:
		if ev.Batch == nil {
			err = fmt.Errorf("entityextract: step %q needs a batch", ev.Step)
		} else if ev.Step == StepPresignBatch {
			out, err = h.PresignBatch(ctx, ev.Run, *ev.Batch)
		} else {
			err = h.PersistBatch(ctx, ev.Run, *ev.Batch)
		}
	case StepFinalize:
		err = h.Finalize(ctx, ev.Run)
	case StepRecordFailure:
		err = h.RecordFailure(ctx, ev.Run)
	default:
		err = fmt.Errorf("entityextract: unknown step %q", ev.Step)
	}
	return out, pipeline.ForLambda(err)
}

func (in Input) key() jobstatus.Key {
	return jobstatus.Key{UserID: in.UserID, DocumentID: in.DocumentID, JobType: queue.JobTypeEntityExtraction}
}

// Plan marks the attempt processing, clears the previous run's entities,
// and stages one input object per batch of chunks.
func (h *Handler) Plan(ctx context.Context, in Input) (Plan, error) {
	ctx = auth.WithUserID(ctx, in.UserID)
	empty := Plan{Batches: []Batch{}}

	if err := h.deps.Status.MarkProcessing(ctx, in.key(), in.Attempt); err != nil {
		if pipeline.Superseded(err) {
			empty.Stale = true
			return empty, nil
		}
		return Plan{}, fmt.Errorf("entityextract: plan: mark processing: %w", pipeline.TransientUnless(err, jobstatus.ErrNotFound))
	}
	if _, err := h.deps.Docs.Get(ctx, in.UserID, in.DocumentID); err != nil {
		return Plan{}, fmt.Errorf("entityextract: plan: get document %s: %w", in.DocumentID, pipeline.TransientUnless(err, document.ErrNotFound))
	}

	chunks, err := h.deps.Chunks.ListByDocument(ctx, in.UserID, in.DocumentID)
	if err != nil {
		return Plan{}, fmt.Errorf("entityextract: plan: list chunks: %w", pipeline.Transient(err))
	}
	if len(chunks) == 0 {
		// Indexing hasn't produced chunks; nothing to extract from.
		empty.NoChunks = true
		return empty, nil
	}

	// Reverse this document's canonical-entity counts before deleting the
	// mentions they're derived from. Both are safe to repeat if this step
	// is retried (DecrementForDocument unlinks the mentions it reverses).
	if err := h.deps.Canonical.DecrementForDocument(ctx, in.UserID, in.DocumentID); err != nil {
		return Plan{}, fmt.Errorf("entityextract: plan: decrement canonical entities: %w", pipeline.Transient(err))
	}
	if err := h.deps.Entities.DeleteByDocument(ctx, in.UserID, in.DocumentID); err != nil {
		return Plan{}, fmt.Errorf("entityextract: plan: clear existing entities: %w", pipeline.Transient(err))
	}

	plan := Plan{Batches: []Batch{}}
	for start := 0; start < len(chunks); start += h.cfg.BatchSize {
		end := min(start+h.cfg.BatchSize, len(chunks))
		b, err := h.stageBatch(ctx, in, len(plan.Batches), chunks[start:end])
		if err != nil {
			return Plan{}, fmt.Errorf("entityextract: plan: %w", err)
		}
		plan.Batches = append(plan.Batches, b)
	}
	return plan, nil
}

// stageBatch uploads one batch's input. Keys include the attempt, so a
// retry never reads a previous attempt's result.
func (h *Handler) stageBatch(ctx context.Context, in Input, index int, chunks []*chunk.Chunk) (Batch, error) {
	req := entitiesRequest{AllowedTypes: h.cfg.AllowedTypes, Chunks: make([]chunkRequest, len(chunks))}
	for i, c := range chunks {
		req.Chunks[i] = chunkRequest{ChunkID: c.ID.String(), Text: c.Text}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Batch{}, fmt.Errorf("marshal batch %d input: %w", index, err)
	}
	prefix := fmt.Sprintf("batch-jobs/entities/%s-%d/batch-%d", in.DocumentID, in.Attempt, index)
	b := Batch{Index: index, InputKey: prefix + "/input.json", ResultKey: prefix + "/result.json"}
	if err := h.deps.Objects.Put(ctx, b.InputKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		return Batch{}, fmt.Errorf("upload batch %d input: %w", index, pipeline.Transient(err))
	}
	return b, nil
}

// PresignBatch presigns b's input and result URLs and names its Batch job.
// Presigning here, per iteration, rather than in Plan keeps a URL's clock
// from starting while earlier batches still hold the GPU queue.
func (h *Handler) PresignBatch(ctx context.Context, in Input, b Batch) (BatchURLs, error) {
	urls := BatchURLs{JobName: fmt.Sprintf("entity-extraction-%s-%d-batch-%d", in.DocumentID, in.Attempt, b.Index)}
	var err error
	if urls.ChunksURL, err = h.deps.Objects.PresignedURL(ctx, b.InputKey, h.cfg.PresignTTL); err != nil {
		return BatchURLs{}, fmt.Errorf("entityextract: presign batch %d input: %w", b.Index, pipeline.Transient(err))
	}
	if urls.ResultURL, err = h.deps.Objects.PresignedPutURL(ctx, b.ResultKey, h.cfg.PresignTTL); err != nil {
		return BatchURLs{}, fmt.Errorf("entityextract: presign batch %d result: %w", b.Index, pipeline.Transient(err))
	}
	return urls, nil
}

// PersistBatch saves the entities b's Batch job extracted. It's a no-op if
// the attempt is no longer current (a newer attempt has cleared and owns
// the document's entities) or if this batch was already persisted (a
// retry after a partial failure).
func (h *Handler) PersistBatch(ctx context.Context, in Input, b Batch) error {
	ctx = auth.WithUserID(ctx, in.UserID)

	// MarkProcessing doubles as the "is this attempt still current?"
	// check: it changes nothing for the current, already-processing
	// attempt.
	if err := h.deps.Status.MarkProcessing(ctx, in.key(), in.Attempt); err != nil {
		if pipeline.Superseded(err) {
			return nil
		}
		return fmt.Errorf("entityextract: persist batch %d: check attempt: %w", b.Index, pipeline.TransientUnless(err, jobstatus.ErrNotFound))
	}

	var req entitiesRequest
	if err := h.readJSON(ctx, b.InputKey, &req); err != nil {
		return fmt.Errorf("entityextract: persist batch %d: input: %w", b.Index, err)
	}
	var resp entitiesResponse
	if err := h.readJSON(ctx, b.ResultKey, &resp); err != nil {
		return fmt.Errorf("entityextract: persist batch %d: result: %w", b.Index, err)
	}

	doc, err := h.deps.Docs.Get(ctx, in.UserID, in.DocumentID)
	if err != nil {
		return fmt.Errorf("entityextract: persist batch %d: get document: %w", b.Index, pipeline.TransientUnless(err, document.ErrNotFound))
	}
	done, err := h.deps.Entities.ChunkIDsWithEntities(ctx, in.UserID, in.DocumentID)
	if err != nil {
		return fmt.Errorf("entityextract: persist batch %d: list extracted chunks: %w", b.Index, pipeline.Transient(err))
	}

	inBatch := make(map[uuid.UUID]bool, len(req.Chunks))
	for _, c := range req.Chunks {
		id, err := uuid.Parse(c.ChunkID)
		if err != nil {
			return fmt.Errorf("entityextract: persist batch %d: bad chunk ID %q in staged input: %w", b.Index, c.ChunkID, err)
		}
		if done[id] {
			// BulkCreate is all-or-nothing, so any chunk with entities
			// means this batch was already saved.
			return nil
		}
		inBatch[id] = true
	}

	entities := make([]*entity.Entity, 0, len(resp.Entities))
	for _, e := range resp.Entities {
		id, err := uuid.Parse(e.ChunkID)
		if err != nil || !inBatch[id] {
			continue // not one of this batch's chunks
		}
		entities = append(entities, &entity.Entity{
			DocumentID: in.DocumentID, KBID: doc.KBID, UserID: in.UserID, ChunkID: id,
			Type: entity.Type(e.Type), Text: e.Text, Start: e.Start, End: e.End, Score: e.Score,
		})
	}
	if len(entities) == 0 {
		return nil
	}
	if err := h.deps.Entities.BulkCreate(ctx, entities); err != nil {
		return fmt.Errorf("entityextract: persist batch %d: save entities: %w", b.Index, pipeline.Transient(err))
	}
	return nil
}

// readJSON decodes the object at key into v. A read failure is transient;
// a decode failure isn't, since the same bytes would fail again.
func (h *Handler) readJSON(ctx context.Context, key string, v any) error {
	rc, err := h.deps.Objects.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read %s: %w", key, pipeline.Transient(err))
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("read %s: %w", key, pipeline.Transient(err))
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode %s: %w", key, err)
	}
	return nil
}

// Finalize queues edge extraction and canonicalization (unless the
// document had no chunks), marks the job succeeded, and deletes the
// staged batch objects. A publish failure is logged, not fatal: the
// entities are saved, and those stages can be re-run on their own.
func (h *Handler) Finalize(ctx context.Context, in Input) error {
	ctx = auth.WithUserID(ctx, in.UserID)
	p := in.Plan
	if p == nil {
		return errors.New("entityextract: finalize: missing plan result")
	}
	if p.Stale {
		return nil
	}

	if !p.NoChunks {
		if err := h.deps.Publisher.PublishEdgeExtraction(ctx, queue.EdgeExtractionRequested{DocumentID: in.DocumentID, UserID: in.UserID}); err != nil {
			log.Printf("entityextract: failed to queue edge extraction for document %s: %v", in.DocumentID, err)
		}
		if err := h.deps.Publisher.PublishCanonicalization(ctx, queue.CanonicalizationRequested{DocumentID: in.DocumentID, UserID: in.UserID}); err != nil {
			log.Printf("entityextract: failed to queue canonicalization for document %s: %v", in.DocumentID, err)
		}
	}

	if err := h.deps.Status.MarkSucceeded(ctx, in.key(), in.Attempt); err != nil && !pipeline.Superseded(err) {
		return fmt.Errorf("entityextract: finalize: mark succeeded: %w", pipeline.TransientUnless(err, jobstatus.ErrNotFound))
	}
	h.deleteStaged(ctx, p)
	return nil
}

// RecordFailure marks the attempt failed and deletes the staged batch
// objects. It leaves the document's status alone: entity extraction is
// independent of whether the document is indexed and searchable. If the
// attempt was superseded by a retry, it does nothing.
func (h *Handler) RecordFailure(ctx context.Context, in Input) error {
	ctx = auth.WithUserID(ctx, in.UserID)
	err := h.deps.Status.MarkFailed(ctx, in.key(), in.Attempt, in.Error.Reason())
	switch {
	case pipeline.Superseded(err):
		return nil
	case err != nil && !errors.Is(err, jobstatus.ErrNotFound):
		return fmt.Errorf("entityextract: record failure: mark attempt failed: %w", pipeline.Transient(err))
	}
	if in.Plan != nil {
		h.deleteStaged(ctx, in.Plan)
	}
	return nil
}

// deleteStaged removes every batch's input and result objects.
// Best-effort: a leaked object costs pennies and isn't worth failing a
// finished run over.
func (h *Handler) deleteStaged(ctx context.Context, p *Plan) {
	for _, b := range p.Batches {
		_ = h.deps.Objects.Delete(ctx, b.InputKey)
		_ = h.deps.Objects.Delete(ctx, b.ResultKey)
	}
}
