package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/llm"
)

// fewShotPrompt is the classification prompt sent to the LLM. It uses
// few-shot examples to teach the model the three route categories without
// any fine-tuning.
const fewShotPrompt = `Classify the question into one of three types:
- normal: a direct factual lookup answered by retrieving the most relevant document chunks.
- aggregation: a question asking what entities appear together or co-occur with a named entity (e.g. "what organizations are mentioned with FEMA", "which people are associated with the climate report").
- multi_hop: a question requiring following connections between entities across documents (e.g. "how is Alice connected to Bob", "what links the WHO to the pandemic response").

Respond with exactly one word: normal, aggregation, or multi_hop.

Examples:
Q: What is the capital of France?
A: normal

Q: What organizations are mentioned alongside FEMA in the disaster report?
A: aggregation

Q: Which companies are associated with the renewable energy initiative?
A: aggregation

Q: How is Dr. Smith connected to the Oxford research group?
A: multi_hop

Q: What links the EPA to the water quality findings?
A: multi_hop

Q: When was the Clean Air Act passed?
A: normal

Q: Summarize the document on climate change.
A: normal

Q: What entities appear together with the World Health Organization?
A: aggregation

Q: %s
A:`

// LLMRouter classifies queries using a language model with few-shot examples.
type LLMRouter struct {
	gen llm.Generator
}

// NewLLMRouter returns an LLMRouter backed by gen.
func NewLLMRouter(gen llm.Generator) *LLMRouter {
	return &LLMRouter{gen: gen}
}

// Route classifies query into a QueryType using few-shot LLM prompting.
// If the model returns an unrecognised label, Route defaults to Normal — a
// missed aggregation question gets an ordinary RAG answer, which is preferable
// to sending a normal question through the expensive graph path.
func (r *LLMRouter) Route(ctx context.Context, query string) (QueryType, error) {
	prompt := fmt.Sprintf(fewShotPrompt, query)
	raw, err := r.gen.Generate(ctx, prompt)
	if err != nil {
		return "", fmt.Errorf("router: generate: %w", err)
	}
	label := strings.TrimSpace(strings.ToLower(raw))
	switch QueryType(label) {
	case Aggregation:
		return Aggregation, nil
	case MultiHop:
		return MultiHop, nil
	default:
		return Normal, nil
	}
}

var _ Router = (*LLMRouter)(nil)
