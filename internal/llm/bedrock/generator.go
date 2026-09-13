// Package bedrock implements llm.Generator against AWS Bedrock's Converse
// API -- the AWS-hosted path to the same Claude models the direct Anthropic
// API calls, billed through AWS rather than an Anthropic API key. Built for
// production's cutover to Bedrock (see the "DNS and TLS cutover plan" dev
// board card): direct Anthropic billing turned out to be a real, immediate
// cost risk (a load-testing burst burned ~$12 of non-refundable credit in
// under an hour), and Bedrock sidesteps that specific exposure entirely --
// staging keeps using the direct Anthropic generator for now, not this one.
package bedrock

import (
	"context"
	"errors"
	"fmt"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/kunalpednekar/dumpster/internal/llm"
)

// maxTokens caps every call's response length, matching the direct
// Anthropic generator's own hardcoded limit -- kept identical rather than
// re-litigated here, since nothing about switching providers changes what
// a reasonable response length is for this app's prompts.
const maxTokens = 4096

// Generator implements llm.Generator using AWS Bedrock's Converse API.
type Generator struct {
	client  *bedrockruntime.Client
	modelID string
}

// New returns a Generator that invokes modelID via Bedrock in region.
// modelID is a Bedrock model ID or inference profile ID/ARN, not the bare
// model name the direct Anthropic API uses -- some models, including
// Claude Sonnet 5, reject on-demand invocation by model ID entirely and
// require an inference profile instead (confirmed for real against this
// account: `aws bedrock-runtime converse --model-id anthropic.claude-
// sonnet-5` fails with ValidationException; `us.anthropic.claude-sonnet-5`,
// the US cross-region inference profile, is what actually works). Credentials
// come from the standard AWS credential chain -- the ECS task role in every
// real deployment, never a static key, matching every other AWS service
// this codebase calls.
func New(ctx context.Context, region, modelID string) (*Generator, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("bedrock: load aws config: %w", err)
	}
	return &Generator{client: bedrockruntime.NewFromConfig(cfg), modelID: modelID}, nil
}

func (g *Generator) Generate(ctx context.Context, prompt string) (string, error) {
	out, err := g.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: &g.modelID,
		Messages: []types.Message{
			{Role: types.ConversationRoleUser, Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: prompt},
			}},
		},
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws32(maxTokens)},
	})
	if err != nil {
		return "", wrapErr("converse", err)
	}
	text, err := outputText(out.Output)
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", fmt.Errorf("bedrock: empty response")
	}
	return text, nil
}

// GenerateStream calls ConverseStream, invoking onDelta for each text delta
// as it arrives -- the same shape as the direct Anthropic generator's
// GenerateStream, just against Bedrock's own streaming event union instead
// of Anthropic's SSE events.
func (g *Generator) GenerateStream(ctx context.Context, prompt string, onDelta func(string)) (string, error) {
	return g.converseStream(ctx, []types.ContentBlock{
		&types.ContentBlockMemberText{Value: prompt},
	}, onDelta)
}

// GenerateStreamCached behaves like GenerateStream, but inserts a cache
// point after cacheablePrefix -- Bedrock's Converse API caches everything
// up to and including a CachePointBlock marker in the content array,
// unlike the direct Anthropic API's cache_control attribute on the block
// itself, but the effect is the same: a repeat call whose prefix matches
// this one gets that portion billed as a cache read instead of full price.
func (g *Generator) GenerateStreamCached(ctx context.Context, cacheablePrefix, dynamicSuffix string, onDelta func(string)) (string, error) {
	return g.converseStream(ctx, []types.ContentBlock{
		&types.ContentBlockMemberText{Value: cacheablePrefix},
		&types.ContentBlockMemberCachePoint{Value: types.CachePointBlock{Type: types.CachePointTypeDefault}},
		&types.ContentBlockMemberText{Value: dynamicSuffix},
	}, onDelta)
}

func (g *Generator) converseStream(ctx context.Context, content []types.ContentBlock, onDelta func(string)) (string, error) {
	out, err := g.client.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{
		ModelId: &g.modelID,
		Messages: []types.Message{
			{Role: types.ConversationRoleUser, Content: content},
		},
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws32(maxTokens)},
	})
	if err != nil {
		return "", wrapErr("converse stream", err)
	}
	stream := out.GetStream()
	defer func() { _ = stream.Close() }()

	var full strings.Builder
	for event := range stream.Events() {
		delta, ok := event.(*types.ConverseStreamOutputMemberContentBlockDelta)
		if !ok {
			continue
		}
		textDelta, ok := delta.Value.Delta.(*types.ContentBlockDeltaMemberText)
		if !ok || textDelta.Value == "" {
			continue
		}
		full.WriteString(textDelta.Value)
		onDelta(textDelta.Value)
	}
	if err := stream.Err(); err != nil {
		return "", fmt.Errorf("bedrock: converse stream: %w", err)
	}
	if full.Len() == 0 {
		return "", fmt.Errorf("bedrock: empty response")
	}
	return full.String(), nil
}

// outputText extracts the first text block from a non-streaming Converse
// response. Bedrock's ConverseOutput is a union (message vs. other variants
// this codebase never triggers, e.g. a guardrail intervention) -- an
// unexpected variant or a message with no text content is treated as
// "nothing came back", the same empty-response error GenerateStream/
// GenerateStreamCached raise, not a silently-empty success.
func outputText(output types.ConverseOutput) (string, error) {
	msgOutput, ok := output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return "", fmt.Errorf("bedrock: unexpected output variant %T", output)
	}
	for _, block := range msgOutput.Value.Content {
		if text, ok := block.(*types.ContentBlockMemberText); ok {
			return text.Value, nil
		}
	}
	return "", nil
}

// wrapErr classifies err before wrapping it with call context. An
// AccessDeniedException is specifically what AWS Budgets' automatic
// APPLY_IAM_POLICY action produces once production's Bedrock spend cap is
// hit -- attaching a Deny policy to this same task role (see the
// "Bedrock spend budget and graceful degradation" dev board card) means
// the very next call is rejected at the authorization step, before any
// request is even processed, not mid-stream. Wrapped with
// llm.ErrProviderQuotaExceeded so a caller (search/answerer.go, etc.) can
// degrade gracefully with errors.Is instead of surfacing a raw AWS error
// or a generic failure worth retrying. Every other error type (a real
// network problem, a malformed request, actual throttling) is left as a
// plain wrapped error -- only an access-denial is treated as "this is the
// spend limit", since assuming every failure is the budget would hide a
// genuinely different, worth-investigating problem behind the same
// reassuring message.
func wrapErr(op string, err error) error {
	var accessDenied *types.AccessDeniedException
	if errors.As(err, &accessDenied) {
		return fmt.Errorf("bedrock: %s: %w: %w", op, llm.ErrProviderQuotaExceeded, err)
	}
	return fmt.Errorf("bedrock: %s: %w", op, err)
}

func aws32(v int32) *int32 { return &v }
