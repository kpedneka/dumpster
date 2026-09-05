// Package inference is the HTTP-client adapter for the consolidated ML
// inference service's /embeddings endpoint, implementing llm.Embedder. It
// now serves query-time search text only -- ingestion-time (bulk) chunk
// embedding moved to internal/llm/awsbatch after this service's shared-cpu
// Fly machine proved too slow under sustained ingestion load. See the
// System Architecture page's Hybrid Cloud sub-page for the full story.
package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/llm"
)

// dims is the dimensionality of BAAI/bge-small-en-v1.5, the model behind
// the inference service's /embeddings endpoint (see scripts/embeddings.py).
const dims = 384

// Embedder implements llm.Embedder via HTTP against the inference
// service's /embeddings endpoint.
type Embedder struct {
	baseURL string
	// isQuery is fixed at construction, not passed to Embed, because
	// llm.Embedder's signature is shared with retrieval/pgstore and
	// retrieval/memory and can't be changed to carry it per call: the BGE
	// model needs its query-instruction prefix applied only to queries
	// (see scripts/embeddings.py), so ingestion and query text are served
	// by two distinctly-constructed Embedders instead.
	isQuery bool
	client  *http.Client
}

// NewDocumentEmbedder returns an Embedder for ingestion-time text (chunks,
// passages) against the inference service at baseURL (e.g.
// "http://inference.internal:8000"). Unused in production today --
// ingestion-time embedding now goes through internal/llm/awsbatch instead --
// kept for its test coverage of the shared HTTP request/response shape.
func NewDocumentEmbedder(baseURL string) *Embedder {
	return newEmbedder(baseURL, false)
}

// NewQueryEmbedder returns an Embedder for query-time search text against
// the inference service at baseURL. See Embedder.isQuery for why this is a
// separate constructor rather than a parameter on Embed itself.
func NewQueryEmbedder(baseURL string) *Embedder {
	return newEmbedder(baseURL, true)
}

func newEmbedder(baseURL string, isQuery bool) *Embedder {
	return &Embedder{baseURL: strings.TrimRight(baseURL, "/"), isQuery: isQuery, client: &http.Client{}}
}

// Dims returns the dimensionality of the embedding vectors this Embedder
// produces.
func (e *Embedder) Dims() int { return dims }

type embedRequest struct {
	Texts   []string `json:"texts"`
	IsQuery bool     `json:"is_query"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Dims       int         `json:"dims"`
}

// Embed returns one embedding per input text, in the same order, via the
// inference service's /embeddings endpoint.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embedRequest{Texts: texts, IsQuery: e.isQuery})
	if err != nil {
		return nil, fmt.Errorf("inference: marshal embeddings request: %w", err)
	}

	respBody, err := e.post(ctx, "/embeddings", body)
	if err != nil {
		return nil, fmt.Errorf("inference: embeddings request: %w", err)
	}

	var resp embedResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("inference: unmarshal embeddings response: %w", err)
	}
	if len(resp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("inference: expected %d embeddings, got %d", len(texts), len(resp.Embeddings))
	}
	return resp.Embeddings, nil
}

// post issues a POST request to path on the inference service, returning
// the response body on a 200 and an error otherwise (including for
// non-2xx statuses, so callers don't need to inspect the status code
// themselves).
func (e *Embedder) post(ctx context.Context, path string, body []byte) ([]byte, error) {
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

var _ llm.Embedder = (*Embedder)(nil)
