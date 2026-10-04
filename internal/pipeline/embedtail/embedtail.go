// Package embedtail is the end of every ingestion state machine, shared by
// document indexing and region classification: once a document's chunks
// are persisted without embeddings, stage their texts for the embedding
// Batch job, then backfill the vectors and mark the document indexed, or
// record the failure.
//
// Each job type's own package (docindex, regionclassify) owns its state
// machine definition and the steps before this point; both call Tail for
// the same behavior after it.
package embedtail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/stats"
)

// Deps are Tail's collaborators. Stats is optional.
type Deps struct {
	Docs   document.Repository
	Chunks chunk.Repository
	// Scratch holds the Batch job's input and result objects (the scratch
	// bucket, not the uploads bucket).
	Scratch objectstore.ObjectStore
	Status  jobstatus.Writer
	Stats   stats.Repository
}

// Tail runs the shared embedding steps.
type Tail struct {
	deps       Deps
	presignTTL time.Duration
}

// New returns a Tail whose presigned URLs last presignTTL (1h if zero),
// long enough to cover the Batch job's queue wait before its container
// starts.
func New(deps Deps, presignTTL time.Duration) *Tail {
	if presignTTL <= 0 {
		presignTTL = time.Hour
	}
	return &Tail{deps: deps, presignTTL: presignTTL}
}

// Run identifies one attempt at one job.
type Run struct {
	JobType    queue.JobType
	DocumentID uuid.UUID
	UserID     uuid.UUID
	Attempt    int
}

// Key is the run's status record key.
func (r Run) Key() jobstatus.Key {
	return jobstatus.Key{UserID: r.UserID, DocumentID: r.DocumentID, JobType: r.JobType}
}

// Staged describes the embedding job's input and result objects, and the
// presigned URLs the job reads and writes them through.
type Staged struct {
	ChunkCount int
	JobName    string
	TextsURL   string
	ResultURL  string
	InputKey   string
	ResultKey  string
}

// embedRequest and embedResponse match scripts/batch_embed_job.py's
// protocol, the same one internal/llm/awsbatch uses.
type embedRequest struct {
	Texts []string `json:"texts"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Stage uploads texts, the persisted chunks' texts in ordinal order, as
// the embedding job's input and presigns its input and result URLs. Keys
// and the job name include the attempt, so a retry never reads a previous
// attempt's result.
func (t *Tail) Stage(ctx context.Context, run Run, texts []string) (Staged, error) {
	body, err := json.Marshal(embedRequest{Texts: texts})
	if err != nil {
		return Staged{}, fmt.Errorf("marshal embed request: %w", err)
	}
	prefix := fmt.Sprintf("batch-jobs/embeddings/%s-%d", run.DocumentID, run.Attempt)
	s := Staged{
		ChunkCount: len(texts),
		JobName:    fmt.Sprintf("embedding-%s-%d", run.DocumentID, run.Attempt),
		InputKey:   prefix + "/input.json",
		ResultKey:  prefix + "/result.json",
	}
	if err := t.deps.Scratch.Put(ctx, s.InputKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		return Staged{}, fmt.Errorf("upload embed input: %w", pipeline.Transient(err))
	}
	if s.TextsURL, err = t.deps.Scratch.PresignedURL(ctx, s.InputKey, t.presignTTL); err != nil {
		return Staged{}, fmt.Errorf("presign embed input: %w", pipeline.Transient(err))
	}
	if s.ResultURL, err = t.deps.Scratch.PresignedPutURL(ctx, s.ResultKey, t.presignTTL); err != nil {
		return Staged{}, fmt.Errorf("presign embed result: %w", pipeline.Transient(err))
	}
	return s, nil
}

// Finalize backfills the chunks' embeddings from staged's result (nil
// when there was nothing to embed), marks the document indexed unless it
// already was before this run, and marks the run succeeded. Staged objects
// and any extra keys are deleted last, so a retry after a partial failure
// can still read the result.
func (t *Tail) Finalize(ctx context.Context, run Run, staged *Staged, alreadyIndexed bool, extraKeys ...string) error {
	if staged != nil {
		if err := t.backfill(ctx, run, staged); err != nil {
			return err
		}
	}
	if !alreadyIndexed {
		if err := t.markIndexed(ctx, run); err != nil {
			return err
		}
	}
	if err := t.deps.Status.MarkSucceeded(ctx, run.Key(), run.Attempt); err != nil && !pipeline.Superseded(err) {
		return fmt.Errorf("mark succeeded: %w", pipeline.TransientUnless(err, jobstatus.ErrNotFound))
	}
	t.Delete(ctx, staged, extraKeys...)
	return nil
}

func (t *Tail) backfill(ctx context.Context, run Run, staged *Staged) error {
	rc, err := t.deps.Scratch.Get(ctx, staged.ResultKey)
	if err != nil {
		return fmt.Errorf("embed result: read object %s: %w", staged.ResultKey, pipeline.Transient(err))
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("embed result: read object %s: %w", staged.ResultKey, pipeline.Transient(err))
	}
	var resp embedResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("unmarshal embed result: %w", err)
	}
	if len(resp.Embeddings) != staged.ChunkCount {
		return fmt.Errorf("embed job returned %d embeddings for %d chunks", len(resp.Embeddings), staged.ChunkCount)
	}

	// Chunks come back in ordinal order, the same order Stage was given
	// their texts in. BulkCreate doesn't return generated IDs, so this is
	// how each vector finds its row.
	persisted, err := t.deps.Chunks.ListByDocument(ctx, run.UserID, run.DocumentID)
	if err != nil {
		return fmt.Errorf("list chunks: %w", pipeline.Transient(err))
	}
	if len(persisted) != len(resp.Embeddings) {
		return fmt.Errorf("%d persisted chunks for %d embeddings", len(persisted), len(resp.Embeddings))
	}
	for i, c := range persisted {
		if err := t.deps.Chunks.UpdateEmbedding(ctx, run.UserID, c.ID, resp.Embeddings[i]); err != nil {
			return fmt.Errorf("update embedding for chunk %s: %w", c.ID, pipeline.Transient(err))
		}
	}
	return nil
}

// markIndexed marks the document indexed and records it in the usage
// stats, unless an earlier, partly failed Finalize already did both.
func (t *Tail) markIndexed(ctx context.Context, run Run) error {
	doc, err := t.deps.Docs.Get(ctx, run.UserID, run.DocumentID)
	if err != nil {
		return fmt.Errorf("get document %s: %w", run.DocumentID, pipeline.TransientUnless(err, document.ErrNotFound))
	}
	if doc.Status == document.StatusIndexed {
		return nil
	}
	if err := t.deps.Docs.UpdateStatus(ctx, run.UserID, run.DocumentID, document.StatusIndexed); err != nil {
		return fmt.Errorf("mark document indexed: %w", pipeline.Transient(err))
	}
	if t.deps.Stats != nil {
		if err := t.deps.Stats.RecordDocumentIndexed(ctx, doc.SizeBytes); err != nil {
			log.Printf("embedtail: failed to record usage stats for document %s: %v", run.DocumentID, err)
		}
	}
	return nil
}

// RecordFailure marks the run and the document failed, then deletes
// staged's objects (if any) and any extra keys. If the run was superseded
// by a retry, the document belongs to the newer attempt and is left alone.
// A document deleted mid-run is not an error.
func (t *Tail) RecordFailure(ctx context.Context, run Run, stepErr *pipeline.StepError, staged *Staged, extraKeys ...string) error {
	err := t.deps.Status.MarkFailed(ctx, run.Key(), run.Attempt, stepErr.Reason())
	switch {
	case pipeline.Superseded(err):
		return nil
	case err != nil && !errors.Is(err, jobstatus.ErrNotFound):
		return fmt.Errorf("mark attempt failed: %w", pipeline.Transient(err))
	}

	err = t.deps.Docs.UpdateStatus(ctx, run.UserID, run.DocumentID, document.StatusFailed)
	if err != nil && !errors.Is(err, document.ErrNotFound) {
		return fmt.Errorf("mark document failed: %w", pipeline.Transient(err))
	}

	t.Delete(ctx, staged, extraKeys...)
	return nil
}

// Delete removes staged's objects (if staged is non-nil) and any extra
// keys from the scratch store. Best-effort: a leaked object costs pennies,
// expires with the bucket's lifecycle rule, and isn't worth failing a
// finished run over.
func (t *Tail) Delete(ctx context.Context, staged *Staged, extraKeys ...string) {
	keys := extraKeys
	if staged != nil {
		keys = append([]string{staged.InputKey, staged.ResultKey}, keys...)
	}
	for _, key := range keys {
		if key != "" {
			_ = t.deps.Scratch.Delete(ctx, key)
		}
	}
}
