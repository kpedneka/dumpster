// Package awsbatch implements worker.LayoutExtractor by submitting an AWS
// Batch job for PDF region extraction, instead of a synchronous HTTP call
// to the always-on inference service's /regions endpoint.
//
// Unlike ingestion-time embedding's move to Batch (see
// internal/llm/awsbatch), this isn't chasing a throttling bug -- region
// extraction (scripts/extract_regions.py: pymupdf + pdfplumber, no ML
// model at all) never had a warm-model benefit to lose by moving off an
// always-on process. The motivation here is pure idle-capacity cost: once
// this and embedding are both off the inference service, the only thing
// left resident there is the small query-time embedding model, letting
// that machine be sized far smaller. See the System Architecture page's
// Hybrid Cloud sub-page for the fuller story this is part of.
//
// Same design as internal/llm/awsbatch: the Batch container never gets
// real object-storage credentials, only short-lived presigned URLs, and
// the job's result travels back via a presigned PUT rather than
// CloudWatch Logs -- a real document's region text can run well past
// CloudWatch's ~256KB single-event cap, the same size concern that ruled
// out logs for embeddings' result.
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
	"github.com/kunalpednekar/dumpster/internal/manifest/layout"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
)

// Config holds the AWS Batch resources this Extractor submits jobs
// against, plus the tunables around polling/URL lifetime. Same shape as
// internal/llm/awsbatch.Config -- a separate type rather than a shared one
// since the two job types are configured against independent AWS Batch
// resources (different job queue/definition) and have no reason to be
// coupled just because the field names happen to match.
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

// Extractor implements worker.LayoutExtractor via AWS Batch.
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

// Response shape matching scripts/batch_regions_job.py's protocol exactly
// -- the same {"regions": [...], "peak_rss_kb": N} shape
// extract_regions.py's own stdin/stdout path and the HTTP /regions
// endpoint both already produce, for parity across all three transports.
type rawRegionJSON struct {
	RegionType  string     `json:"region_type"`
	PageNumber  int        `json:"page_number"`
	BoundingBox [4]float64 `json:"bbox"`
	Text        string     `json:"text"`
	ImageBase64 string     `json:"image_base64"`
	NeedsVLM    string     `json:"needs_vlm"`
}

type regionsResponse struct {
	Regions   []rawRegionJSON `json:"regions"`
	PeakRSSKB int             `json:"peak_rss_kb"`
}

// ExtractRegions classifies all regions in pdfBytes via a single AWS Batch
// job, returning them in the same order scripts/extract_regions.py
// produces them (page number, top-to-bottom).
func (e *Extractor) ExtractRegions(ctx context.Context, pdfBytes []byte) ([]*layout.RawRegion, error) {
	if len(pdfBytes) == 0 {
		return nil, nil
	}

	shortSuffix := uuid.New().String()[:8]
	pdfKey := fmt.Sprintf("batch-jobs/regions/%s/input.pdf", shortSuffix)
	resultKey := fmt.Sprintf("batch-jobs/regions/%s/result.json", shortSuffix)
	if err := e.store.Put(ctx, pdfKey, bytes.NewReader(pdfBytes), int64(len(pdfBytes)), "application/pdf"); err != nil {
		return nil, fmt.Errorf("awsbatch: upload input pdf: %w", err)
	}
	defer func() {
		// Best-effort: leaked temp objects cost pennies and aren't worth
		// failing an otherwise-successful extraction over. Uses a
		// cancellation-detached context since ctx may already be near its
		// deadline by the time this returns.
		_ = e.store.Delete(context.WithoutCancel(ctx), pdfKey)
		_ = e.store.Delete(context.WithoutCancel(ctx), resultKey)
	}()

	pdfURL, err := e.store.PresignedURL(ctx, pdfKey, e.cfg.PresignTTL)
	if err != nil {
		return nil, fmt.Errorf("awsbatch: presign input: %w", err)
	}
	resultURL, err := e.store.PresignedPutURL(ctx, resultKey, e.cfg.PresignTTL)
	if err != nil {
		return nil, fmt.Errorf("awsbatch: presign result: %w", err)
	}

	jobID, err := e.client.SubmitJob(ctx, awsbatch.SubmitJobParams{
		JobName:       fmt.Sprintf("region-extraction-%s", shortSuffix),
		JobQueue:      e.cfg.JobQueue,
		JobDefinition: e.cfg.JobDefinition,
		Environment:   map[string]string{"PDF_URL": pdfURL, "RESULT_URL": resultURL},
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

	var resp regionsResponse
	if err := json.Unmarshal(resultBody, &resp); err != nil {
		return nil, fmt.Errorf("awsbatch: job %s: unmarshal regions response: %w", jobID, err)
	}

	out := make([]*layout.RawRegion, 0, len(resp.Regions))
	for _, r := range resp.Regions {
		out = append(out, &layout.RawRegion{
			RegionType:  r.RegionType,
			PageNumber:  r.PageNumber,
			BoundingBox: r.BoundingBox,
			Text:        r.Text,
			ImageBase64: r.ImageBase64,
			NeedsVLM:    r.NeedsVLM,
		})
	}
	return out, nil
}

// waitForCompletion polls jobID's status until it reaches a terminal
// state or ctx is done. Same shape as internal/llm/awsbatch's identical
// helper -- no shared implementation between the two packages since
// there's nothing else in either to couple them on.
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
