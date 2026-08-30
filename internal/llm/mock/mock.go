package mock

import (
	"context"
	"fmt"
)

// Embedder is a test double for llm.Embedder.
type Embedder struct {
	dims    int
	EmbedFn func(ctx context.Context, texts []string) ([][]float32, error)
}

func NewEmbedder(dims int) *Embedder {
	return &Embedder{
		dims: dims,
		EmbedFn: func(_ context.Context, texts []string) ([][]float32, error) {
			out := make([][]float32, len(texts))
			for i := range texts {
				out[i] = make([]float32, dims)
			}
			return out, nil
		},
	}
}

func (m *Embedder) Dims() int { return m.dims }
func (m *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return m.EmbedFn(ctx, texts)
}

// Generator is a test double for llm.Generator.
type Generator struct {
	GenerateFn func(ctx context.Context, prompt string) (string, error)
	// GenerateStreamFn overrides GenerateStream's default behavior (calling
	// GenerateFn and delivering its result as a single delta). Set this in
	// tests that need to exercise multi-delta streaming behavior.
	GenerateStreamFn func(ctx context.Context, prompt string, onDelta func(string)) (string, error)
}

func NewGenerator(response string) *Generator {
	return &Generator{
		GenerateFn: func(_ context.Context, _ string) (string, error) {
			return response, nil
		},
	}
}

func NewErrorGenerator(msg string) *Generator {
	return &Generator{
		GenerateFn: func(_ context.Context, _ string) (string, error) {
			return "", fmt.Errorf("%s", msg)
		},
	}
}

func (m *Generator) Generate(ctx context.Context, prompt string) (string, error) {
	return m.GenerateFn(ctx, prompt)
}

// GenerateStream delegates to GenerateStreamFn if set, otherwise falls back
// to calling GenerateFn and delivering the whole result as a single delta.
func (m *Generator) GenerateStream(ctx context.Context, prompt string, onDelta func(string)) (string, error) {
	if m.GenerateStreamFn != nil {
		return m.GenerateStreamFn(ctx, prompt, onDelta)
	}
	text, err := m.GenerateFn(ctx, prompt)
	if err != nil {
		return "", err
	}
	onDelta(text)
	return text, nil
}
