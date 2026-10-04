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
	"context"
	_ "embed"
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
	"github.com/kunalpednekar/dumpster/internal/pipeline/embedtail"
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
	Docs document.Repository
	// Uploads is the store user documents are read from.
	Uploads objectstore.ObjectStore
	// Scratch holds the embedding job's input and result objects.
	Scratch  objectstore.ObjectStore
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
	tail *embedtail.Tail
}

// New returns a Handler.
func New(deps Deps, cfg Config) *Handler {
	return &Handler{deps: deps, tail: embedtail.New(embedtail.Deps{
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

func (in Input) run() embedtail.Run {
	return embedtail.Run{JobType: queue.JobTypeDocumentIndexing, DocumentID: in.DocumentID, UserID: in.UserID, Attempt: in.Attempt}
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

// Prepare marks the attempt processing, splits the document into chunks,
// persists them without embeddings, publishes entity extraction, and
// stages the chunk texts for the embedding job.
func (h *Handler) Prepare(ctx context.Context, in Input) (Prepared, error) {
	ctx = auth.WithUserID(ctx, in.UserID)

	if err := h.deps.Status.MarkProcessing(ctx, in.run().Key(), in.Attempt); err != nil {
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

	st, err := h.tail.Stage(ctx, in.run(), texts)
	if err != nil {
		return Prepared{}, fmt.Errorf("docindex: prepare: %w", err)
	}
	return Prepared{
		Embed: true, ChunkCount: st.ChunkCount, JobName: st.JobName, TextsURL: st.TextsURL,
		ResultURL: st.ResultURL, InputKey: st.InputKey, ResultKey: st.ResultKey,
	}, nil
}

func (h *Handler) readObject(ctx context.Context, key string) ([]byte, error) {
	rc, err := h.deps.Uploads.Get(ctx, key)
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
func (h *Handler) Finalize(ctx context.Context, in Input) error {
	ctx = auth.WithUserID(ctx, in.UserID)
	p := in.Prepare
	if p == nil {
		return errors.New("docindex: finalize: missing prepare result")
	}
	if p.Stale {
		return nil
	}
	if err := h.tail.Finalize(ctx, in.run(), p.staged(), p.AlreadyIndexed); err != nil {
		return fmt.Errorf("docindex: finalize: %w", err)
	}
	return nil
}

// RecordFailure marks the job attempt and the document failed, then
// deletes any staged objects. If the attempt was superseded by a retry,
// the document belongs to the newer attempt and is left alone.
func (h *Handler) RecordFailure(ctx context.Context, in Input) error {
	ctx = auth.WithUserID(ctx, in.UserID)
	if err := h.tail.RecordFailure(ctx, in.run(), in.Error, in.Prepare.staged()); err != nil {
		return fmt.Errorf("docindex: record failure: %w", err)
	}
	return nil
}
