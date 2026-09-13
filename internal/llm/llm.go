package llm

import (
	"context"
	"errors"
)

// ErrProviderQuotaExceeded is returned by a Generator when the underlying
// provider has refused a call because a spend or quota limit was hit --
// e.g. AWS Budgets' automatic IAM-deny action attaching to the ECS task
// role once production's Bedrock spend cap is reached (see the "Bedrock
// spend budget and graceful degradation" dev board card). Provider-
// agnostic on purpose: a caller checking for this (errors.Is) shouldn't
// need to know or care which Generator implementation is behind the
// interface, only that the answer is "not available right now because of
// a spend limit" rather than a generic failure worth retrying or
// surfacing as a raw error.
var ErrProviderQuotaExceeded = errors.New("llm: provider quota or spend limit exceeded")

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
	// GenerateStreamCached behaves like GenerateStream, but marks cacheablePrefix
	// as eligible for the provider's prompt-caching, so repeat calls sharing
	// the same prefix verbatim are billed at a fraction of the normal input-
	// token rate for that portion. dynamicSuffix is appended after the
	// cached block, uncached -- the part that genuinely varies per call (the
	// caller's actual question, not the shared context it's asked against).
	// Intended for callers whose prefix is large and likely to recur across
	// nearby calls (e.g. retrieved source chunks reused across similar
	// queries against the same knowledge base); a small, one-off prompt
	// gets no benefit from this over GenerateStream (providers also enforce
	// a minimum cacheable size below which a cache breakpoint is silently a
	// no-op, not an error).
	GenerateStreamCached(ctx context.Context, cacheablePrefix, dynamicSuffix string, onDelta func(delta string)) (string, error)
}
