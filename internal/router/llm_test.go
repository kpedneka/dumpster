package router_test

import (
	"context"
	"testing"

	llmmock "github.com/kunalpednekar/dumpster/internal/llm/mock"
	"github.com/kunalpednekar/dumpster/internal/router"
)

func TestLLMRouter_Normal(t *testing.T) {
	gen := llmmock.NewGenerator("normal")
	r := router.NewLLMRouter(gen)

	qt, err := r.Route(context.Background(), "What is the capital of France?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qt != router.Normal {
		t.Errorf("got %q, want Normal", qt)
	}
}

func TestLLMRouter_Aggregation(t *testing.T) {
	gen := llmmock.NewGenerator("aggregation")
	r := router.NewLLMRouter(gen)

	qt, err := r.Route(context.Background(), "What organizations are mentioned with FEMA?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qt != router.Aggregation {
		t.Errorf("got %q, want Aggregation", qt)
	}
}

func TestLLMRouter_MultiHop(t *testing.T) {
	gen := llmmock.NewGenerator("multi_hop")
	r := router.NewLLMRouter(gen)

	qt, err := r.Route(context.Background(), "How is Dr. Smith connected to the Oxford group?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qt != router.MultiHop {
		t.Errorf("got %q, want MultiHop", qt)
	}
}

// TestLLMRouter_UnknownLabelDefaultsToNormal verifies that an unrecognised
// model response falls back to Normal rather than returning an error.
func TestLLMRouter_UnknownLabelDefaultsToNormal(t *testing.T) {
	gen := llmmock.NewGenerator("banana")
	r := router.NewLLMRouter(gen)

	qt, err := r.Route(context.Background(), "anything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if qt != router.Normal {
		t.Errorf("got %q, want Normal as safe default", qt)
	}
}

// TestLLMRouter_WhitespaceStripped verifies leading/trailing whitespace and
// mixed case in the model's response are normalised before matching.
func TestLLMRouter_WhitespaceStripped(t *testing.T) {
	cases := []struct {
		raw  string
		want router.QueryType
	}{
		{"  Aggregation\n", router.Aggregation},
		{"\tMULTI_HOP ", router.MultiHop},
		{"  NORMAL  ", router.Normal},
	}
	for _, tc := range cases {
		gen := llmmock.NewGenerator(tc.raw)
		r := router.NewLLMRouter(gen)
		qt, err := r.Route(context.Background(), "q")
		if err != nil {
			t.Fatalf("raw=%q: unexpected error: %v", tc.raw, err)
		}
		if qt != tc.want {
			t.Errorf("raw=%q: got %q, want %q", tc.raw, qt, tc.want)
		}
	}
}

// TestLLMRouter_GeneratorError verifies that generator errors are propagated.
func TestLLMRouter_GeneratorError(t *testing.T) {
	gen := llmmock.NewErrorGenerator("llm unavailable")
	r := router.NewLLMRouter(gen)

	_, err := r.Route(context.Background(), "query")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

var _ router.Router = (*router.LLMRouter)(nil)
