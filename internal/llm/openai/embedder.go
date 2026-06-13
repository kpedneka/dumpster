package openai

import (
	"context"
	"fmt"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

const defaultDims = 1536 // text-embedding-3-small

// Embedder implements llm.Embedder using the OpenAI Embeddings API.
type Embedder struct {
	client openai.Client
	model  string
	dims   int
}

func New(apiKey, model string) *Embedder {
	client := openai.NewClient(option.WithAPIKey(apiKey))
	return &Embedder{client: client, model: model, dims: defaultDims}
}

func (e *Embedder) Dims() int { return e.dims }

func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	resp, err := e.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts},
		Model: openai.EmbeddingModel(e.model),
	})
	if err != nil {
		return nil, fmt.Errorf("openai: embed: %w", err)
	}
	out := make([][]float32, len(resp.Data))
	for i, d := range resp.Data {
		vec := make([]float32, len(d.Embedding))
		for j, v := range d.Embedding {
			vec[j] = float32(v)
		}
		out[i] = vec
	}
	return out, nil
}
