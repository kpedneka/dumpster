// Package docindex implements the Lambda steps of the document indexing
// state machine (statemachine.asl.json in this package), which replaces
// the worker's DocumentHandler:
//
//  1. Prepare: split the document into chunks, persist them without
//     embeddings, publish entity extraction (which only needs chunk text,
//     so it can run while embedding does), and stage the chunk texts in S3
//     for the embedding Batch job.
//  2. Embed: the state machine runs the Batch job itself
//     (batch:submitJob.sync), so no Lambda waits on it.
//  3. Finalize: backfill each chunk's embedding from the job's result,
//     then mark the document indexed and the job succeeded.
//  4. RecordFailure: every other state's Catch lands here. It marks the
//     document and the job attempt failed, which is what the UI's retry
//     button acts on.
//
// Each step is idempotent, because a transient failure retries it. A step
// running for an attempt that's no longer current (the user already
// retried) does nothing.
package docindex

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
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/stats"
)

// The steps the state machine's Lambda tasks send in Event.Step.
const (
	StepPrepare       = "prepare"
	StepFinalize      = "finalize"
	StepRecordFailure = "record_failure"
)

// Definition is the state machine definition, in Amazon States Language,
// with ${name} placeholders for the deployment-specific values listed in
// DefinitionVars. Terraform fills them with templatefile() on this same
// file; RenderDefinition does the same in Go.
//
//go:embed statemachine.asl.json
var Definition string

// DefinitionVars are the placeholders in Definition: the Lambda running
// this package's Handler, and the Batch queue and job definition for the
// embedding job.
var DefinitionVars = []string{"lambda_arn", "embed_job_queue", "embed_job_definition"}

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
// attempt}). Prepare's result is added at $.prepare, and a caught error at
// $.error.
type Input struct {
	Type       queue.JobType `json:"type"`
	DocumentID uuid.UUID     `json:"document_id"`
	UserID     uuid.UUID     `json:"user_id"`
	Attempt    int           `json:"attempt"`
	Prepare    *Prepared     `json:"prepare,omitempty"`
	Error      *StepError    `json:"error,omitempty"`
}

// Prepared is Prepare's result. The state machine reads Embed to decide
// whether to run the Batch job, and passes JobName, TextsURL and ResultURL
// to it.
type Prepared struct {
	// Stale means this attempt is no longer current, so later steps do
	// nothing.
	Stale bool `json:"stale,omitempty"`
	// AlreadyIndexed means the document was indexed before this run, so
	// Finalize only records success.
	AlreadyIndexed bool `json:"already_indexed,omitempty"`
	// Embed means chunks were persisted and need embedding.
	Embed      bool   `json:"embed"`
	ChunkCount int    `json:"chunk_count,omitempty"`
	JobName    string `json:"job_name,omitempty"`
	// TextsURL and ResultURL are the presigned GET and PUT URLs the Batch
	// container reads its input from and writes its result to.
	TextsURL  string `json:"texts_url,omitempty"`
	ResultURL string `json:"result_url,omitempty"`
	// InputKey and ResultKey are the objects behind those URLs, deleted
	// once the run finishes either way.
	InputKey  string `json:"input_key,omitempty"`
	ResultKey string `json:"result_key,omitempty"`
}

// StepError is what a state's Catch records at $.error.
type StepError = pipeline.StepError

// Deps are the Handler's collaborators. Stats is optional.
type Deps struct {
	Docs     document.Repository
	Objects  objectstore.ObjectStore
	Chunks   chunk.Repository
	Splitter chunk.Splitter
	// Publisher publishes entity extraction once chunks exist.
	Publisher queue.Publisher
	Status    jobstatus.Writer
	Stats     stats.Repository
}

// Config holds the Handler's tunables.
type Config struct {
	// PresignTTL is how long the Batch job's input and result URLs stay
	// valid, covering its queue wait and cold start. Defaults to 1h.
	PresignTTL time.Duration
}

// Handler runs the document indexing state machine's Lambda steps.
type Handler struct {
	deps Deps
	cfg  Config
}

// New returns a Handler.
func New(deps Deps, cfg Config) *Handler {
	if cfg.PresignTTL <= 0 {
		cfg.PresignTTL = time.Hour
	}
	return &Handler{deps: deps, cfg: cfg}
}

// embedRequest and embedResponse match scripts/batch_embed_job.py's
// protocol, the same one internal/llm/awsbatch uses.
type embedRequest struct {
	Texts []string `json:"texts"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Handle routes ev to its step. It's the Lambda handler's body: errors
// come back through pipeline.ForLambda, so transient ones reach the state
// machine as TransientError.
func (h *Handler) Handle(ctx context.Context, ev Event) (any, error) {
	var out any
	var err error
	switch ev.Step {
	case StepPrepare:
		out, err = h.Prepare(ctx, ev.Run)
	case StepFinalize:
		err = h.Finalize(ctx, ev.Run)
	case StepRecordFailure:
		err = h.RecordFailure(ctx, ev.Run)
	default:
		err = fmt.Errorf("docindex: unknown step %q", ev.Step)
	}
	return out, pipeline.ForLambda(err)
}

func (in Input) key() jobstatus.Key {
	return jobstatus.Key{UserID: in.UserID, DocumentID: in.DocumentID, JobType: queue.JobTypeDocumentIndexing}
}

// Prepare marks the attempt processing, splits the document into chunks,
// persists them without embeddings, publishes entity extraction, and
// stages the chunk texts for the embedding job.
func (h *Handler) Prepare(ctx context.Context, in Input) (Prepared, error) {
	ctx = auth.WithUserID(ctx, in.UserID)

	if err := h.deps.Status.MarkProcessing(ctx, in.key(), in.Attempt); err != nil {
		if pipeline.Superseded(err) {
			return Prepared{Stale: true}, nil
		}
		return Prepared{}, fmt.Errorf("docindex: prepare: mark processing: %w", pipeline.TransientUnless(err, jobstatus.ErrNotFound))
	}

	doc, err := h.deps.Docs.Get(ctx, in.UserID, in.DocumentID)
	if err != nil {
		return Prepared{}, fmt.Errorf("docindex: prepare: get document %s: %w", in.DocumentID, pipeline.TransientUnless(err, document.ErrNotFound))
	}
	if doc.Status == document.StatusIndexed {
		return Prepared{AlreadyIndexed: true}, nil
	}
	if err := h.deps.Docs.UpdateStatus(ctx, in.UserID, in.DocumentID, document.StatusProcessing); err != nil {
		return Prepared{}, fmt.Errorf("docindex: prepare: mark document processing: %w", pipeline.Transient(err))
	}

	data, err := h.readObject(ctx, doc.S3Key)
	if err != nil {
		return Prepared{}, fmt.Errorf("docindex: prepare: %w", err)
	}

	splits := h.deps.Splitter.Split(string(data))
	if len(splits) == 0 {
		h.publishEntityExtraction(ctx, doc)
		return Prepared{}, nil
	}

	// Replace any chunks a previous attempt or a retried Prepare left.
	if err := h.deps.Chunks.DeleteByDocument(ctx, in.UserID, in.DocumentID); err != nil {
		return Prepared{}, fmt.Errorf("docindex: prepare: clear existing chunks: %w", pipeline.Transient(err))
	}
	texts := make([]string, len(splits))
	for i, s := range splits {
		s.DocumentID, s.KBID, s.UserID = doc.ID, doc.KBID, doc.UserID
		texts[i] = s.Text
	}
	if err := h.deps.Chunks.BulkCreate(ctx, splits); err != nil {
		return Prepared{}, fmt.Errorf("docindex: prepare: persist chunks: %w", pipeline.Transient(err))
	}

	h.publishEntityExtraction(ctx, doc)

	p, err := h.stageEmbedInput(ctx, in, texts)
	if err != nil {
		return Prepared{}, fmt.Errorf("docindex: prepare: %w", err)
	}
	return p, nil
}

func (h *Handler) readObject(ctx context.Context, key string) ([]byte, error) {
	rc, err := h.deps.Objects.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("read object %s: %w", key, pipeline.Transient(err))
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read object %s: %w", key, pipeline.Transient(err))
	}
	return data, nil
}

// stageEmbedInput uploads texts for the Batch job and presigns its input
// and result URLs. Keys and the job name include the attempt, so a retry
// never reads a previous attempt's result.
func (h *Handler) stageEmbedInput(ctx context.Context, in Input, texts []string) (Prepared, error) {
	body, err := json.Marshal(embedRequest{Texts: texts})
	if err != nil {
		return Prepared{}, fmt.Errorf("marshal embed request: %w", err)
	}
	prefix := fmt.Sprintf("batch-jobs/embeddings/%s-%d", in.DocumentID, in.Attempt)
	p := Prepared{
		Embed:      true,
		ChunkCount: len(texts),
		JobName:    fmt.Sprintf("embedding-%s-%d", in.DocumentID, in.Attempt),
		InputKey:   prefix + "/input.json",
		ResultKey:  prefix + "/result.json",
	}
	if err := h.deps.Objects.Put(ctx, p.InputKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		return Prepared{}, fmt.Errorf("upload embed input: %w", pipeline.Transient(err))
	}
	if p.TextsURL, err = h.deps.Objects.PresignedURL(ctx, p.InputKey, h.cfg.PresignTTL); err != nil {
		return Prepared{}, fmt.Errorf("presign embed input: %w", pipeline.Transient(err))
	}
	if p.ResultURL, err = h.deps.Objects.PresignedPutURL(ctx, p.ResultKey, h.cfg.PresignTTL); err != nil {
		return Prepared{}, fmt.Errorf("presign embed result: %w", pipeline.Transient(err))
	}
	return p, nil
}

// publishEntityExtraction queues entity extraction for doc. A failure is
// logged, not fatal: indexing can still succeed, and the user can re-run
// entity extraction on its own, which is what the worker did too.
func (h *Handler) publishEntityExtraction(ctx context.Context, doc *document.Document) {
	if err := h.deps.Publisher.PublishEntityExtraction(ctx, queue.EntityExtractionRequested{
		DocumentID: doc.ID, UserID: doc.UserID,
	}); err != nil {
		log.Printf("docindex: failed to queue entity extraction for document %s: %v", doc.ID, err)
	}
}

// Finalize backfills the chunks' embeddings from the Batch job's result
// (if one ran), marks the document indexed, and marks the job succeeded.
// The staged objects are deleted last, so a retry after a partial failure
// can still read the result.
func (h *Handler) Finalize(ctx context.Context, in Input) error {
	ctx = auth.WithUserID(ctx, in.UserID)
	p := in.Prepare
	if p == nil {
		return errors.New("docindex: finalize: missing prepare result")
	}
	if p.Stale {
		return nil
	}

	if p.Embed {
		if err := h.backfillEmbeddings(ctx, in, p); err != nil {
			return fmt.Errorf("docindex: finalize: %w", err)
		}
	}

	if !p.AlreadyIndexed {
		if err := h.markIndexed(ctx, in); err != nil {
			return fmt.Errorf("docindex: finalize: %w", err)
		}
	}

	if err := h.deps.Status.MarkSucceeded(ctx, in.key(), in.Attempt); err != nil && !pipeline.Superseded(err) {
		return fmt.Errorf("docindex: finalize: mark succeeded: %w", pipeline.TransientUnless(err, jobstatus.ErrNotFound))
	}

	h.deleteStaged(ctx, p)
	return nil
}

func (h *Handler) backfillEmbeddings(ctx context.Context, in Input, p *Prepared) error {
	body, err := h.readObject(ctx, p.ResultKey)
	if err != nil {
		return fmt.Errorf("embed result: %w", err)
	}
	var resp embedResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("unmarshal embed result: %w", err)
	}
	if len(resp.Embeddings) != p.ChunkCount {
		return fmt.Errorf("embed job returned %d embeddings for %d chunks", len(resp.Embeddings), p.ChunkCount)
	}

	// Chunks come back in ordinal order, the same order Prepare staged
	// their texts in. BulkCreate doesn't return generated IDs, so this is
	// how each vector finds its row.
	persisted, err := h.deps.Chunks.ListByDocument(ctx, in.UserID, in.DocumentID)
	if err != nil {
		return fmt.Errorf("list chunks: %w", pipeline.Transient(err))
	}
	if len(persisted) != len(resp.Embeddings) {
		return fmt.Errorf("%d persisted chunks for %d embeddings", len(persisted), len(resp.Embeddings))
	}
	for i, c := range persisted {
		if err := h.deps.Chunks.UpdateEmbedding(ctx, in.UserID, c.ID, resp.Embeddings[i]); err != nil {
			return fmt.Errorf("update embedding for chunk %s: %w", c.ID, pipeline.Transient(err))
		}
	}
	return nil
}

// markIndexed marks the document indexed and records it in the usage
// stats, unless an earlier, partly failed Finalize already did both.
func (h *Handler) markIndexed(ctx context.Context, in Input) error {
	doc, err := h.deps.Docs.Get(ctx, in.UserID, in.DocumentID)
	if err != nil {
		return fmt.Errorf("get document %s: %w", in.DocumentID, pipeline.TransientUnless(err, document.ErrNotFound))
	}
	if doc.Status == document.StatusIndexed {
		return nil
	}
	if err := h.deps.Docs.UpdateStatus(ctx, in.UserID, in.DocumentID, document.StatusIndexed); err != nil {
		return fmt.Errorf("mark document indexed: %w", pipeline.Transient(err))
	}
	if h.deps.Stats != nil {
		if err := h.deps.Stats.RecordDocumentIndexed(ctx, doc.SizeBytes); err != nil {
			log.Printf("docindex: failed to record usage stats for document %s: %v", in.DocumentID, err)
		}
	}
	return nil
}

// RecordFailure marks the job attempt and the document failed, then
// deletes any staged objects. If the attempt was superseded by a retry,
// the document belongs to the newer attempt and is left alone. A document
// deleted mid-run is not an error.
func (h *Handler) RecordFailure(ctx context.Context, in Input) error {
	ctx = auth.WithUserID(ctx, in.UserID)

	err := h.deps.Status.MarkFailed(ctx, in.key(), in.Attempt, in.Error.Reason())
	switch {
	case pipeline.Superseded(err):
		return nil
	case err != nil && !errors.Is(err, jobstatus.ErrNotFound):
		return fmt.Errorf("docindex: record failure: mark attempt failed: %w", pipeline.Transient(err))
	}

	err = h.deps.Docs.UpdateStatus(ctx, in.UserID, in.DocumentID, document.StatusFailed)
	if err != nil && !errors.Is(err, document.ErrNotFound) {
		return fmt.Errorf("docindex: record failure: mark document failed: %w", pipeline.Transient(err))
	}

	if in.Prepare != nil {
		h.deleteStaged(ctx, in.Prepare)
	}
	return nil
}

// deleteStaged removes the embed job's input and result objects.
// Best-effort: a leaked object costs pennies and isn't worth failing a
// finished run over.
func (h *Handler) deleteStaged(ctx context.Context, p *Prepared) {
	for _, key := range []string{p.InputKey, p.ResultKey} {
		if key != "" {
			_ = h.deps.Objects.Delete(ctx, key)
		}
	}
}
