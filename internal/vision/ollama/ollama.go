// Package ollama is the sole adapter package permitted to call the local
// Ollama VLM service. It implements vision.Describer via the Ollama
// /api/generate endpoint using plain net/http + encoding/json (no new
// Go dependency). The VLM is used only for (a) describing confirmed figures
// and (b) a yes/no skip-confirmation for suspected scanned content.
//
// Ambiguity and network errors both default to the skip-safe outcome
// (empty description for Describe, isScanned=true for ConfirmScanned),
// matching the spec's "honestly skip rather than guess" bar.
package ollama

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/vision"
)

// Describer implements vision.Describer using Ollama's /api/generate
// endpoint.
type Describer struct {
	baseURL string
	model   string
	client  *http.Client
}

// New returns a Describer that calls the Ollama service at baseURL using
// the given VLM model name. baseURL should be e.g. "http://ollama:11434".
func New(baseURL, model string) *Describer {
	return &Describer{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{},
	}
}

// newWithClient is used in tests to inject a custom HTTP client.
func newWithClient(baseURL, model string, client *http.Client) *Describer {
	return &Describer{baseURL: strings.TrimRight(baseURL, "/"), model: model, client: client}
}

type generateRequest struct {
	Model  string   `json:"model"`
	Prompt string   `json:"prompt"`
	Images []string `json:"images,omitempty"` // base64-encoded image data
	Stream bool     `json:"stream"`
}

type generateResponse struct {
	Response string `json:"response"`
}

func (d *Describer) generate(ctx context.Context, prompt string, imageBytes []byte) (string, error) {
	req := generateRequest{
		Model:  d.model,
		Prompt: prompt,
		Stream: false,
	}
	if len(imageBytes) > 0 {
		req.Images = []string{base64.StdEncoding.EncodeToString(imageBytes)}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ollama: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("%w: %w", vision.ErrVLMUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: status %d", vision.ErrVLMUnavailable, resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ollama: read response: %w", err)
	}

	var gen generateResponse
	if err := json.Unmarshal(respBytes, &gen); err != nil {
		return "", fmt.Errorf("ollama: unmarshal response: %w", err)
	}
	return strings.TrimSpace(gen.Response), nil
}

// Describe asks the VLM to describe the content of image. Returns "" if the
// model produces an empty response or the service is unavailable (caller
// marks the region skipped).
func (d *Describer) Describe(ctx context.Context, image []byte) (string, error) {
	const describePrompt = "Describe the content of this figure or image in 1-3 sentences. Focus on what information it conveys rather than its visual style."
	desc, err := d.generate(ctx, describePrompt, image)
	if err != nil {
		// Unavailable is not fatal — region will be skipped.
		return "", nil
	}
	return desc, nil
}

// ConfirmScanned asks the VLM whether image is a rasterized/scanned page
// element. Returns true (isScanned) on any error or ambiguous response,
// matching the "honestly skip rather than guess" bar.
func (d *Describer) ConfirmScanned(ctx context.Context, image []byte) (bool, error) {
	const confirmPrompt = "Is this image a scanned or photographed page of text or a table that cannot be extracted as digital text? Answer only 'yes' or 'no'."
	resp, err := d.generate(ctx, confirmPrompt, image)
	if err != nil {
		// Default to skip-safe: treat as scanned when uncertain.
		return true, nil
	}
	lower := strings.ToLower(strings.TrimSpace(resp))
	// Treat any response starting with "no" as "not scanned" (extractable).
	isScanned := !strings.HasPrefix(lower, "no")
	return isScanned, nil
}

var _ vision.Describer = (*Describer)(nil)
