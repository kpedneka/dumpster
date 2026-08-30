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
	// GenerateStream behaves like Generate but also invokes onDelta for each
	// text chunk as it arrives, letting a caller forward partial progress
	// (e.g. to a streaming HTTP response) instead of waiting for the full
	// completion. It still returns the full accumulated text once
	// generation completes, identically to what Generate would return for
	// the same prompt.
	GenerateStream(ctx context.Context, prompt string, onDelta func(delta string)) (string, error)
}
