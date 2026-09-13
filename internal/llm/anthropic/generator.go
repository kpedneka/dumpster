package anthropic

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Generator implements llm.Generator using the Anthropic Messages API.
type Generator struct {
	client anthropic.Client
	model  string
}

// New returns a Generator authenticated with apiKey, using model for every
// call. Extra opts are passed through to the underlying SDK client — tests
// use this to inject a fake HTTP transport via option.WithHTTPClient.
func New(apiKey, model string, opts ...option.RequestOption) *Generator {
	clientOpts := append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	client := anthropic.NewClient(clientOpts...)
	return &Generator{client: client, model: model}
}

func (g *Generator) Generate(ctx context.Context, prompt string) (string, error) {
	msg, err := g.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(g.model),
		MaxTokens: 4096,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("anthropic: generate: %w", err)
	}
	if len(msg.Content) == 0 {
		return "", fmt.Errorf("anthropic: empty response")
	}
	return msg.Content[0].Text, nil
}

// GenerateStream calls the Messages API's streaming endpoint, invoking
// onDelta for each text_delta event's text as it arrives. Non-text delta
// variants (e.g. thinking deltas) are ignored — this codebase never enables
// extended thinking, so only text_delta is ever expected here.
func (g *Generator) GenerateStream(ctx context.Context, prompt string, onDelta func(string)) (string, error) {
	stream := g.client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(g.model),
		MaxTokens: 4096,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	defer func() { _ = stream.Close() }()

	var full strings.Builder
	for stream.Next() {
		event := stream.Current()
		blockDelta, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent)
		if !ok {
			continue
		}
		textDelta, ok := blockDelta.Delta.AsAny().(anthropic.TextDelta)
		if !ok || textDelta.Text == "" {
			continue
		}
		full.WriteString(textDelta.Text)
		onDelta(textDelta.Text)
	}
	if err := stream.Err(); err != nil {
		return "", fmt.Errorf("anthropic: generate stream: %w", err)
	}
	if full.Len() == 0 {
		return "", fmt.Errorf("anthropic: empty response")
	}
	return full.String(), nil
}

// GenerateStreamCached behaves like GenerateStream, but sends cacheablePrefix
// and dynamicSuffix as two separate content blocks in the same user message,
// with a cache_control breakpoint on the first. Anthropic caches everything
// up to and including a marked block, so a repeat call whose prefix matches
// this one byte-for-byte gets that portion billed as a cache read (a
// fraction of normal input pricing) instead of full price -- the same
// message shape a single-string call would produce, just split so the
// provider knows where the stable, worth-reusing part ends.
func (g *Generator) GenerateStreamCached(ctx context.Context, cacheablePrefix, dynamicSuffix string, onDelta func(string)) (string, error) {
	cached := anthropic.TextBlockParam{
		Text:         cacheablePrefix,
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
	}
	stream := g.client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(g.model),
		MaxTokens: 4096,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(
				anthropic.ContentBlockParamUnion{OfText: &cached},
				anthropic.NewTextBlock(dynamicSuffix),
			),
		},
	})
	defer func() { _ = stream.Close() }()

	var full strings.Builder
	for stream.Next() {
		event := stream.Current()
		blockDelta, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent)
		if !ok {
			continue
		}
		textDelta, ok := blockDelta.Delta.AsAny().(anthropic.TextDelta)
		if !ok || textDelta.Text == "" {
			continue
		}
		full.WriteString(textDelta.Text)
		onDelta(textDelta.Text)
	}
	if err := stream.Err(); err != nil {
		return "", fmt.Errorf("anthropic: generate stream cached: %w", err)
	}
	if full.Len() == 0 {
		return "", fmt.Errorf("anthropic: empty response")
	}
	return full.String(), nil
}
