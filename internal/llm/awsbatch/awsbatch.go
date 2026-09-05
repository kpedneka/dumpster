// Package awsbatch implements llm.Embedder by submitting an AWS Batch job
// for CPU-only local embedding, for ingestion-time text only (chunks,
// passages) -- query-time search embedding stays on the always-on Fly
// inference service via internal/llm/inference.NewQueryEmbedder, since a
// per-request Fargate cold start would be unacceptable for a live search.
//
// Moved off the HTTP-based internal/llm/inference.NewDocumentEmbedder
// after measuring it directly against Fly's shared-cpu tier: a 47.5s
// embedding call there took 13-16+ minutes in production, tracked down to
// Fly throttling shared vCPUs to a 5ms/80ms baseline quota once burst
// balance empties -- a scheduler-level CPU denial no in-process thread
// tuning could fix. See scripts/embed_bench_job.py for the experiment
// that established this and the vCPU-size comparison behind the chosen
// job definition's sizing.
//
// Same design as internal/entity/awsbatch in most respects: this package
// never touches Postgres, and the Batch container gets a short-lived
// presigned URL rather than real object-storage credentials. It diverges
// on one specific point, though: the job's RESULT travels back via a
// presigned PUT to object storage, not CloudWatch Logs the way entity
// extraction's result does. That was the first design tried here too, and
// it broke in production: a single document's embedding response (433
// texts x 384 floats, full JSON precision) runs a few MB, but CloudWatch
// Logs caps a single log event at ~256KB. AWS's own log driver silently
// splits an oversized print() line across multiple events with no marker
// that they were ever one line, so JobLogs() (which joins events with
// "\n", correct for the normal one-event-per-line case) reassembles it
// with spurious newlines in the middle of the JSON -- reliably producing
// "unexpected end of JSON input" for any real document, not an
// intermittent failure. Entity extraction's response is small enough
// this never bit it; embeddings' isn't, so the result needed a transport
// with no practical size ceiling.
//
// Also unlike entity extraction, there is no multi-batch submit-all-then-
// await concurrency here: a single AWS Batch job handles an entire
// document's chunks in one call (validated directly -- 433 texts in one
// job, no batching needed), since CPU Fargate jobs don't have GPU entity
// extraction's cold-start-amortization-across-concurrent-jobs concern in
// the first place.
package awsbatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/awsbatch"
	"github.com/kunalpednekar/dumpster/internal/llm"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
)

// dims is the dimensionality of BAAI/bge-small-en-v1.5, the model behind
// this job (see scripts/embeddings.py) -- matches
// internal/llm/inference's own dims constant for the HTTP path.
const dims = 384

// Config holds the AWS Batch resources this Embedder submits jobs
// against, plus the tunables around polling/URL lifetime.
type Config struct {
	JobQueue      string
	JobDefinition string
	// PollInterval is how often to check a submitted job's status.
	// Defaults to 5s if zero.
	PollInterval time.Duration
	// PresignTTL is how long the input/result URLs handed to the job stay
	// valid. Defaults to 15m if zero.
	PresignTTL time.Duration
}

// Embedder implements llm.Embedder via AWS Batch.
type Embedder struct {
	client awsbatch.Client
	store  objectstore.ObjectStore
	cfg    Config
}

// New returns an Embedder submitting jobs via client, using store for the
// input/result handoff.
func New(client awsbatch.Client, store objectstore.ObjectStore, cfg Config) *Embedder {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.PresignTTL <= 0 {
		cfg.PresignTTL = 15 * time.Minute
	}
	return &Embedder{client: client, store: store, cfg: cfg}
}

// Dims returns the dimensionality of the embedding vectors this Embedder
// produces.
func (e *Embedder) Dims() int { return dims }

// Request/response shapes matching scripts/batch_embed_job.py's protocol
// exactly.
type embedRequest struct {
	Texts []string `json:"texts"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Dims       int         `json:"dims"`
}

// Embed returns one embedding per input text, in the same order, via a
// single AWS Batch job.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embedRequest{Texts: texts})
	if err != nil {
		return nil, fmt.Errorf("awsbatch: marshal embed request: %w", err)
	}

	// No document ID is available at this layer (llm.Embedder's signature
	// is plain []string, not chunk-aware) -- a random suffix is all that's
	// needed for S3 key/job-name uniqueness here, unlike entity
	// extraction's per-document-traceable naming.
	shortSuffix := uuid.New().String()[:8]
	inputKey := fmt.Sprintf("batch-jobs/embeddings/%s/input.json", shortSuffix)
	resultKey := fmt.Sprintf("batch-jobs/embeddings/%s/result.json", shortSuffix)
	if err := e.store.Put(ctx, inputKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		return nil, fmt.Errorf("awsbatch: upload input: %w", err)
	}
	defer func() {
		// Best-effort: leaked temp objects cost pennies and aren't worth
		// failing an otherwise-successful embed over. Uses a
		// cancellation-detached context since ctx may already be near its
		// deadline by the time this returns.
		_ = e.store.Delete(context.WithoutCancel(ctx), inputKey)
		_ = e.store.Delete(context.WithoutCancel(ctx), resultKey)
	}()

	inputURL, err := e.store.PresignedURL(ctx, inputKey, e.cfg.PresignTTL)
	if err != nil {
		return nil, fmt.Errorf("awsbatch: presign input: %w", err)
	}
	resultURL, err := e.store.PresignedPutURL(ctx, resultKey, e.cfg.PresignTTL)
	if err != nil {
		return nil, fmt.Errorf("awsbatch: presign result: %w", err)
	}

	jobID, err := e.client.SubmitJob(ctx, awsbatch.SubmitJobParams{
		JobName:       fmt.Sprintf("embedding-%s", shortSuffix),
		JobQueue:      e.cfg.JobQueue,
		JobDefinition: e.cfg.JobDefinition,
		Environment:   map[string]string{"TEXTS_URL": inputURL, "RESULT_URL": resultURL},
	})
	if err != nil {
		return nil, fmt.Errorf("awsbatch: submit job: %w", err)
	}

	if err := e.waitForCompletion(ctx, jobID); err != nil {
		return nil, err
	}

	rc, err := e.store.Get(ctx, resultKey)
	if err != nil {
		return nil, fmt.Errorf("awsbatch: job %s: fetch result: %w", jobID, err)
	}
	defer func() { _ = rc.Close() }()
	resultBody, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("awsbatch: job %s: read result: %w", jobID, err)
	}

	var resp embedResponse
	if err := json.Unmarshal(resultBody, &resp); err != nil {
		return nil, fmt.Errorf("awsbatch: job %s: unmarshal embed response: %w", jobID, err)
	}
	if len(resp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("awsbatch: job %s: expected %d embeddings, got %d", jobID, len(texts), len(resp.Embeddings))
	}
	return resp.Embeddings, nil
}

// waitForCompletion polls jobID's status until it reaches a terminal
// state or ctx is done. No separate, shorter timeout hardcoded here --
// this blocks for as long as the caller's context allows, matching
// internal/entity/awsbatch's same choice.
func (e *Embedder) waitForCompletion(ctx context.Context, jobID string) error {
	ticker := time.NewTicker(e.cfg.PollInterval)
	defer ticker.Stop()

	for {
		state, err := e.client.JobState(ctx, jobID)
		if err != nil {
			return fmt.Errorf("awsbatch: poll job %s: %w", jobID, err)
		}
		switch state.Status {
		case awsbatch.StatusSucceeded:
			return nil
		case awsbatch.StatusFailed:
			return fmt.Errorf("awsbatch: job %s failed: %s", jobID, state.Reason)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

var _ llm.Embedder = (*Embedder)(nil)
