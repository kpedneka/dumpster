// Package layout is the vendor adapter for PDF region extraction. It
// implements region classification by calling the consolidated ML
// inference service's /regions endpoint over HTTP (v4.10) — previously
// this shelled out to scripts/extract_regions.py as its own warm
// subprocess embedded inside cmd/worker; see the System Architecture
// page's Inference Service sub-page for the full design and why that
// changed.
//
// This adapter covers layers 1 (pdfplumber: native text/tables) and 2
// (unstructured.io: layout segmentation of whatever pdfplumber cannot
// resolve). A third layer, VLM-based figure description and scanned-content
// confirmation, was scaffolded in RegionClassificationHandler but never
// finished being wired up and has since been removed; see that handler's
// type doc for why.
package layout

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// Config configures an Extractor.
type Config struct {
	// BaseURL is the inference service's base URL (e.g.
	// "http://inference.internal:8000").
	BaseURL string
	// Logger receives per-run peak-RSS observations (see v4.6): the fixed
	// ~390MB floor that drove the worker's 512MB->1024MB memory bump was
	// found by reading OOM-kill log lines for jobs that crashed. This gives
	// the other half of the picture — peak RSS for jobs that complete
	// normally — so a future sizing decision can be based on a measured
	// distribution instead of the handful of crashes that happened to get
	// logged. Defaults to the standard logger when nil.
	Logger *log.Logger
}

// RawRegion is a single classified region from the inference service.
// The RegionClassificationHandler routes each RawRegion through additional
// Go-side steps before persisting it to the ingestion manifest.
type RawRegion struct {
	// RegionType is the coarse label assigned by the layered classifier:
	// "native_text", "native_table", "figure", "unconfirmed_text", or
	// "unconfirmed_table".
	RegionType string
	// PageNumber is 1-indexed.
	PageNumber int
	// BoundingBox holds fractional page coordinates in [0,1]: [x0,y0,x1,y1].
	BoundingBox [4]float64
	// Text is set for native_text and native_table regions where the
	// classifier could extract readable text directly.
	Text string
	// ImageBase64 is a base64-encoded PNG crop of the region, set for figure
	// and unconfirmed_* regions that need a VLM follow-up call.
	ImageBase64 string
	// NeedsVLM classifies why layers 1+2 couldn't fully resolve this region:
	//   "describe"      — a figure/image (no VLM description; see
	//                     RegionClassificationHandler's type doc).
	//   "confirm_scan"  — suspected scanned content (no VLM confirmation;
	//                     treated as skipped rather than guessed).
	//   ""              — no follow-up needed (native content already resolved).
	NeedsVLM string
}

// Extractor implements region extraction via HTTP against the inference
// service's /regions endpoint.
type Extractor struct {
	cfg    Config
	client *http.Client
}

// New returns an Extractor calling the inference service at cfg.BaseURL.
func New(cfg Config) *Extractor {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &Extractor{cfg: cfg, client: &http.Client{}}
}

type request struct {
	PDFB64 string `json:"pdf_base64"`
}

type rawRegionJSON struct {
	RegionType  string     `json:"region_type"`
	PageNumber  int        `json:"page_number"`
	BoundingBox [4]float64 `json:"bbox"`
	Text        string     `json:"text"`
	ImageBase64 string     `json:"image_base64"`
	NeedsVLM    string     `json:"needs_vlm"`
}

type response struct {
	Regions []rawRegionJSON `json:"regions"`
	// PeakRSSKB is the inference service process's peak resident set size
	// in KB at the time of this request. Since v4.8 that process is shared
	// and always-on, this is no longer a per-job figure the way it was
	// under the old per-document subprocess model — kept for continuity of
	// the sizing signal, but treat it as "the service's RSS so far", not
	// "this document's incremental cost".
	PeakRSSKB int `json:"peak_rss_kb"`
}

// ExtractRegions classifies all regions in pdfBytes and returns them in
// reading order (page number, top-to-bottom). The returned RawRegion slice
// includes both resolved regions (Text set) and unresolved regions
// (ImageBase64 set, NeedsVLM non-empty) so the caller can invoke VLM follow-up
// only where needed.
func (e *Extractor) ExtractRegions(ctx context.Context, pdfBytes []byte) ([]*RawRegion, error) {
	if len(pdfBytes) == 0 {
		return nil, nil
	}

	req := request{PDFB64: base64.StdEncoding.EncodeToString(pdfBytes)}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("layout: marshal request: %w", err)
	}

	respBody, err := e.post(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("layout: regions request: %w", err)
	}

	var resp response
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("layout: unmarshal response: %w", err)
	}
	e.cfg.Logger.Printf("layout: extraction peak RSS: %d KB", resp.PeakRSSKB)

	out := make([]*RawRegion, 0, len(resp.Regions))
	for _, r := range resp.Regions {
		out = append(out, &RawRegion{
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

// post issues a POST /regions request to the inference service, returning
// the response body on a 200 and an error otherwise.
func (e *Extractor) post(ctx context.Context, body []byte) ([]byte, error) {
	url := strings.TrimRight(e.cfg.BaseURL, "/") + "/regions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, respBody)
	}
	return respBody, nil
}
