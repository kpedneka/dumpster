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
	"strings"
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

// Extract runs entity extraction over chunks via an AWS Batch job,
// restricted to allowedTypes, and maps the result back onto entity.Entity
// rows populated with each input chunk's DocumentID/KBID/UserID.
func (e *Extractor) Extract(ctx context.Context, chunks []*chunk.Chunk, allowedTypes []entity.Type) ([]*entity.Entity, error) {
	if len(chunks) == 0 || len(allowedTypes) == 0 {
		return nil, nil
	}

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

	jobUUID := uuid.New().String()
	inputKey := fmt.Sprintf("batch-jobs/entities/%s/input.json", jobUUID)
	if err := e.store.Put(ctx, inputKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		return nil, fmt.Errorf("awsbatch: upload input: %w", err)
	}
	defer func() {
		// Best-effort: a leaked temp object costs pennies and isn't worth
		// failing an otherwise-successful extraction over. Uses a
		// cancellation-detached context since ctx may already be near its
		// deadline by the time Extract returns.
		_ = e.store.Delete(context.WithoutCancel(ctx), inputKey)
	}()

	inputURL, err := e.store.PresignedURL(ctx, inputKey, e.cfg.PresignTTL)
	if err != nil {
		return nil, fmt.Errorf("awsbatch: presign input: %w", err)
	}

	jobID, err := e.client.SubmitJob(ctx, awsbatch.SubmitJobParams{
		JobName:       "entity-extraction-" + jobUUID,
		JobQueue:      e.cfg.JobQueue,
		JobDefinition: e.cfg.JobDefinition,
		Environment:   map[string]string{"CHUNKS_URL": inputURL},
	})
	if err != nil {
		return nil, fmt.Errorf("awsbatch: submit job: %w", err)
	}

	if err := e.waitForCompletion(ctx, jobID); err != nil {
		return nil, err
	}

	logs, err := e.client.JobLogs(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("awsbatch: fetch job %s logs: %w", jobID, err)
	}

	resultLine, err := extractResultLine(logs)
	if err != nil {
		return nil, fmt.Errorf("awsbatch: job %s: %w", jobID, err)
	}

	var resp entitiesResponse
	if err := json.Unmarshal([]byte(resultLine), &resp); err != nil {
		return nil, fmt.Errorf("awsbatch: job %s: unmarshal entities response: %w", jobID, err)
	}

	out := make([]*entity.Entity, 0, len(resp.Entities))
	for _, re := range resp.Entities {
		c, ok := byID[re.ChunkID]
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
	return out, nil
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

var _ entity.Extractor = (*Extractor)(nil)
