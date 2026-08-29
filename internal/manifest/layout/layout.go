// Package layout is the vendor adapter for PDF region extraction. It is the
// only place a dependency on the pdfplumber+unstructured Python stack may be
// touched: this adapter shells out to scripts/extract_regions.py via
// os/exec, passing the base64-encoded PDF on stdin and reading back the
// classified region list as JSON on stdout.
//
// This adapter covers layers 1 (pdfplumber: native text/tables) and 2
// (unstructured.io: layout segmentation of whatever pdfplumber cannot
// resolve). Layer 3 (VLM via Ollama) runs Go-side in the
// RegionClassificationHandler and is never called from this package.
package layout

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
)

// Config configures an Extractor.
type Config struct {
	// PythonPath is the interpreter used to run ScriptPath.
	PythonPath string
	// ScriptPath is the path to scripts/extract_regions.py.
	ScriptPath string
	// Logger receives per-run peak-RSS observations (see v4.6): the fixed
	// ~390MB floor that drove the worker's 512MB->1024MB memory bump was
	// found by reading OOM-kill log lines for jobs that crashed. This gives
	// the other half of the picture — peak RSS for jobs that complete
	// normally — so a future sizing decision can be based on a measured
	// distribution instead of the handful of crashes that happened to get
	// logged. Defaults to the standard logger when nil.
	Logger *log.Logger
}

// RawRegion is a single classified region from the Python extraction script.
// The RegionClassificationHandler routes each RawRegion through additional
// Go-side steps (VLM calls for figures and suspected-scanned content) before
// persisting it to the ingestion manifest.
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
	// NeedsVLM is a directive for the Go-side handler:
	//   "describe"      — call vision.Describer.Describe (for figures).
	//   "confirm_scan"  — call vision.Describer.ConfirmScanned (for suspected scans).
	//   ""              — no VLM needed (native content already resolved).
	NeedsVLM string
}

// Extractor implements region extraction by shelling out to the Python script.
type Extractor struct {
	cfg Config
	// runCommand is overridable in tests to exercise the JSON I/O contract
	// without a real Python environment.
	runCommand func(ctx context.Context, pythonPath, scriptPath string, stdin []byte) ([]byte, error)
}

// New returns an Extractor configured to invoke cfg.PythonPath cfg.ScriptPath.
func New(cfg Config) *Extractor {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &Extractor{cfg: cfg, runCommand: runPython}
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
	// PeakRSSKB is the Python subprocess's own peak RSS in KB, reported only
	// on successful completion (a killed job never reaches this point).
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
	stdin, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("layout: marshal request: %w", err)
	}

	stdout, err := e.runCommand(ctx, e.cfg.PythonPath, e.cfg.ScriptPath, stdin)
	if err != nil {
		return nil, fmt.Errorf("layout: run extraction script: %w", err)
	}

	var resp response
	if err := json.Unmarshal(stdout, &resp); err != nil {
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

// runPython executes pythonPath scriptPath, writing stdin to the process's
// stdin and returning its stdout.
func runPython(ctx context.Context, pythonPath, scriptPath string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, pythonPath, scriptPath)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}
