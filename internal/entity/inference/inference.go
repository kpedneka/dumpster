// Package inference is the HTTP-client adapter for the consolidated ML
// inference service (v4.8). It implements entity.Extractor by calling that
// service's /entities endpoint over HTTP, replacing the os/exec-based
// subprocess sidecar (internal/entity/gliner, retired by this change) that
// used to embed a warm Python process inside cmd/worker itself. See the
// System Architecture page's Inference Service sub-page for the full
// design and why it changed.
package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/entity"
)

// Extractor implements entity.Extractor via HTTP against the inference
// service's /entities endpoint.
type Extractor struct {
	baseURL string
	client  *http.Client
}

// New returns an Extractor calling the inference service at baseURL (e.g.
// "http://inference.internal:8000").
func New(baseURL string) *Extractor {
	return &Extractor{baseURL: strings.TrimRight(baseURL, "/"), client: &http.Client{}}
}

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

// Extract runs entity extraction over chunks via the inference service,
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
		return nil, fmt.Errorf("inference: marshal entities request: %w", err)
	}

	respBody, err := e.post(ctx, "/entities", body)
	if err != nil {
		return nil, fmt.Errorf("inference: entities request: %w", err)
	}

	var resp entitiesResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("inference: unmarshal entities response: %w", err)
	}

	out := make([]*entity.Entity, 0, len(resp.Entities))
	for _, re := range resp.Entities {
		c, ok := byID[re.ChunkID]
		if !ok {
			// The service returned an entity for a chunk we didn't send;
			// skip rather than fail the whole batch.
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

// post issues a POST request to path on the inference service, returning
// the response body on a 200 and an error otherwise (including for
// non-2xx statuses, so callers don't need to inspect the status code
// themselves).
func (e *Extractor) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+path, bytes.NewReader(body))
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

var _ entity.Extractor = (*Extractor)(nil)
