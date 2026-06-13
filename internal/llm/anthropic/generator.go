package anthropic

import (
	"context"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Generator implements llm.Generator using the Anthropic Messages API.
type Generator struct {
	client anthropic.Client
	model  string
}

func New(apiKey, model string) *Generator {
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
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
