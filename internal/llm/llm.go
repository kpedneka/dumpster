package llm

import "context"

// Embedder converts text into dense vector representations.
type Embedder interface {
	// Embed returns one embedding per input text, in the same order.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dims returns the dimensionality of the embedding vectors.
	Dims() int
}

// Generator produces text completions from a prompt.
type Generator interface {
	Generate(ctx context.Context, prompt string) (string, error)
}
