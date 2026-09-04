// Package awsbatch implements entity.Extractor by submitting an AWS Batch
// job for GPU entity extraction — the only entity.Extractor implementation
// in this codebase; there is no warm-HTTP-service fallback (that path,
// internal/entity/inference, was removed once GPU throughput became the
// accepted baseline for both local dev and production, not just an
// optional speedup). Also replaces an earlier stop/start-able EC2 box
// design, dropped once it turned out fast wake latency didn't actually
// matter, since entity extraction isn't on the path to a document's
// visible "indexed" status.
//
// This package never touches Postgres or grants the Batch container real
// object-storage credentials — see scripts/batch_entity_job.py's own
// docstring for the matching half of this design. The container gets a
// short-lived presigned GET URL for its input and prints its result to
// stdout for this package to read back via CloudWatch Logs, not a second
// storage round trip.
package awsbatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/awsbatch"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/entity"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
)

// resultMarker is the line prefix scripts/batch_entity_job.py uses to mark
// its actual result line in job output, distinguishing it from
// extract_entities.py's own timing print (which also goes to stdout).
const resultMarker = "BATCH_RESULT: "

// Config holds the AWS Batch resources this Extractor submits jobs
// against, plus the tunables around polling/URL lifetime.
type Config struct {
	JobQueue      string
	JobDefinition string
	// PollInterval is how often to check a submitted job's status.
	// Defaults to 5s if zero.
	PollInterval time.Duration
	// PresignTTL is how long the input URL handed to the job stays valid.
	// Defaults to 15m if zero — comfortably longer than Batch's own queue
	// wait plus cold-start time, per the real numbers measured during the
	// hand-rolled EC2 pilot this design replaced.
	PresignTTL time.Duration
}

// Extractor implements entity.Extractor via AWS Batch.
type Extractor struct {
	client awsbatch.Client
	store  objectstore.ObjectStore
	cfg    Config
}

// New returns an Extractor submitting jobs via client, using store for the
// input/result handoff.
func New(client awsbatch.Client, store objectstore.ObjectStore, cfg Config) *Extractor {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.PresignTTL <= 0 {
		cfg.PresignTTL = 15 * time.Minute
	}
	return &Extractor{client: client, store: store, cfg: cfg}
}

// Request/response shapes matching extract_entities.py's stdin protocol
// exactly.
type entitiesRequest struct {
	AllowedTypes []string       `json:"allowed_types"`
	Chunks       []chunkRequest `json:"chunks"`
}

type chunkRequest struct {
	ChunkID string `json:"chunk_id"`
	Text    string `json:"text"`
}

type entitiesResponse struct {
	Entities []entityResponse `json:"entities"`
}

type entityResponse struct {
	ChunkID string  `json:"chunk_id"`
	Type    string  `json:"type"`
	Text    string  `json:"text"`
	Start   int     `json:"start"`
	End     int     `json:"end"`
	Score   float32 `json:"score"`
}

// Extract runs entity extraction over chunks via a single AWS Batch job,
// restricted to allowedTypes, and maps the result back onto entity.Entity
// rows populated with each input chunk's DocumentID/KBID/UserID.
func (e *Extractor) Extract(ctx context.Context, chunks []*chunk.Chunk, allowedTypes []entity.Type) ([]*entity.Entity, error) {
	if len(chunks) == 0 || len(allowedTypes) == 0 {
		return nil, nil
	}
	sub, err := e.submit(ctx, 0, chunks, allowedTypes)
	if err != nil {
		return nil, err
	}
	result := e.await(ctx, sub)
	return result.Entities, result.Err
}

// ExtractBatches runs entity extraction over several independent batches
// of chunks, submitting an AWS Batch job for every batch before waiting on
// any of them, then awaiting all of them concurrently. See
// entity.BatchExtractor's doc for why submitting up front rather than one
// batch at a time matters here specifically.
func (e *Extractor) ExtractBatches(ctx context.Context, batches [][]*chunk.Chunk, allowedTypes []entity.Type) []entity.BatchResult {
	results := make([]entity.BatchResult, len(batches))
	if len(allowedTypes) == 0 {
		return results
	}

	submissions := make([]*submission, len(batches))
	for i, batch := range batches {
		if len(batch) == 0 {
			continue
		}
		sub, err := e.submit(ctx, i, batch, allowedTypes)
		if err != nil {
			results[i] = entity.BatchResult{Err: err}
			continue
		}
		submissions[i] = sub
	}

	var wg sync.WaitGroup
	for i, sub := range submissions {
		if sub == nil {
			continue
		}
		wg.Add(1)
		go func(i int, sub *submission) {
			defer wg.Done()
			results[i] = e.await(ctx, sub)
		}(i, sub)
	}
	wg.Wait()

	return results
}

// submission is one batch's in-flight AWS Batch job: submitted, not yet
// awaited.
type submission struct {
	jobID       string
	inputKey    string
	byID        map[string]*chunk.Chunk
	batchIdx    int
	submittedAt time.Time
}

// submit uploads batchIdx's input, submits its AWS Batch job, and returns
// a handle to await it -- the "submit" half of what Extract used to do in
// one synchronous call. batchIdx (and the batch's DocumentID, read off the
// first chunk) name the job and its S3 input key so a real backlog of
// concurrently-running jobs for the same document is identifiable from
// `aws batch list-jobs` output alone, without cross-referencing the
// Postgres jobs table -- a real, live debugging gap this closes.
func (e *Extractor) submit(ctx context.Context, batchIdx int, chunks []*chunk.Chunk, allowedTypes []entity.Type) (*submission, error) {
	byID := make(map[string]*chunk.Chunk, len(chunks))
	req := entitiesRequest{
		AllowedTypes: make([]string, len(allowedTypes)),
		Chunks:       make([]chunkRequest, len(chunks)),
	}
	for i, t := range allowedTypes {
		req.AllowedTypes[i] = string(t)
	}
	for i, c := range chunks {
		id := c.ID.String()
		byID[id] = c
		req.Chunks[i] = chunkRequest{ChunkID: id, Text: c.Text}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("awsbatch: marshal entities request: %w", err)
	}

	documentID := chunks[0].DocumentID
	shortSuffix := uuid.New().String()[:8]
	inputKey := fmt.Sprintf("batch-jobs/entities/%s/batch-%d-%s/input.json", documentID, batchIdx, shortSuffix)
	if err := e.store.Put(ctx, inputKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		return nil, fmt.Errorf("awsbatch: upload input: %w", err)
	}

	inputURL, err := e.store.PresignedURL(ctx, inputKey, e.cfg.PresignTTL)
	if err != nil {
		_ = e.store.Delete(context.WithoutCancel(ctx), inputKey)
		return nil, fmt.Errorf("awsbatch: presign input: %w", err)
	}

	jobID, err := e.client.SubmitJob(ctx, awsbatch.SubmitJobParams{
		JobName:       fmt.Sprintf("entity-extraction-%s-batch-%d", documentID, batchIdx),
		JobQueue:      e.cfg.JobQueue,
		JobDefinition: e.cfg.JobDefinition,
		Environment:   map[string]string{"CHUNKS_URL": inputURL},
	})
	if err != nil {
		_ = e.store.Delete(context.WithoutCancel(ctx), inputKey)
		return nil, fmt.Errorf("awsbatch: submit job: %w", err)
	}

	return &submission{jobID: jobID, inputKey: inputKey, byID: byID, batchIdx: batchIdx, submittedAt: time.Now()}, nil
}

// await waits for sub's job to reach a terminal state, fetches and parses
// its result, and maps it back onto entity.Entity rows -- the "await" half
// of what Extract used to do in one synchronous call. Always cleans up the
// job's input object, on both success and failure.
//
// Logs a per-phase timing breakdown for every batch -- added specifically
// to diagnose a measured ~2.6-minute tail latency on ExtractBatches calls,
// where the whole call is gated by whichever single batch is slowest.
// Candidate causes were AWS Batch's own job-status-transition propagation
// (waitForCompletion) vs. CloudWatch Logs' delivery lag (JobLogs) -- this
// makes which one actually dominates observable per real run instead of
// guessed at.
func (e *Extractor) await(ctx context.Context, sub *submission) entity.BatchResult {
	defer func() {
		// Best-effort: a leaked temp object costs pennies and isn't worth
		// failing an otherwise-successful extraction over. Uses a
		// cancellation-detached context since ctx may already be near its
		// deadline by the time this returns.
		_ = e.store.Delete(context.WithoutCancel(ctx), sub.inputKey)
	}()

	waitStart := time.Now()
	waitErr := e.waitForCompletion(ctx, sub.jobID)
	waitElapsed := time.Since(waitStart)

	if waitErr != nil {
		log.Printf("awsbatch: batch %d (job %s): waitForCompletion failed after %s: %v", sub.batchIdx, sub.jobID, waitElapsed, waitErr)
		return entity.BatchResult{Err: waitErr}
	}

	logsStart := time.Now()
	logs, err := e.client.JobLogs(ctx, sub.jobID)
	logsElapsed := time.Since(logsStart)
	totalElapsed := time.Since(sub.submittedAt)
	log.Printf("awsbatch: batch %d (job %s): waitForCompletion=%s JobLogs=%s totalSinceSubmit=%s",
		sub.batchIdx, sub.jobID, waitElapsed, logsElapsed, totalElapsed)
	if err != nil {
		return entity.BatchResult{Err: fmt.Errorf("awsbatch: fetch job %s logs: %w", sub.jobID, err)}
	}

	resultLine, err := extractResultLine(logs)
	if err != nil {
		return entity.BatchResult{Err: fmt.Errorf("awsbatch: job %s: %w", sub.jobID, err)}
	}

	var resp entitiesResponse
	if err := json.Unmarshal([]byte(resultLine), &resp); err != nil {
		return entity.BatchResult{Err: fmt.Errorf("awsbatch: job %s: unmarshal entities response: %w", sub.jobID, err)}
	}

	out := make([]*entity.Entity, 0, len(resp.Entities))
	for _, re := range resp.Entities {
		c, ok := sub.byID[re.ChunkID]
		if !ok {
			// The job returned an entity for a chunk we didn't send; skip
			// rather than fail the whole batch.
			continue
		}
		out = append(out, &entity.Entity{
			DocumentID: c.DocumentID,
			KBID:       c.KBID,
			UserID:     c.UserID,
			ChunkID:    c.ID,
			Type:       entity.Type(re.Type),
			Text:       re.Text,
			Start:      re.Start,
			End:        re.End,
			Score:      re.Score,
		})
	}
	return entity.BatchResult{Entities: out}
}

// waitForCompletion polls jobID's status until it reaches a terminal
// state or ctx is done. Fast-wake latency was the reason this whole
// pipeline stage moved off a wake/sleep EC2 box onto Batch in the first
// place, so this blocks for as long as the caller's context allows —
// there is deliberately no separate, shorter timeout hardcoded here.
func (e *Extractor) waitForCompletion(ctx context.Context, jobID string) error {
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

// extractResultLine finds batch_entity_job.py's marked result line in a
// job's captured output. Its absence (most likely a BATCH_ERROR: line
// instead, printed on a fetch or extraction failure inside the job) is
// surfaced as an error carrying the full log, so a failure is debuggable
// from the returned error alone.
func extractResultLine(logs string) (string, error) {
	for _, line := range strings.Split(logs, "\n") {
		if strings.HasPrefix(line, resultMarker) {
			return strings.TrimPrefix(line, resultMarker), nil
		}
	}
	return "", fmt.Errorf("no result line in job output: %s", logs)
}

var _ entity.BatchExtractor = (*Extractor)(nil)
