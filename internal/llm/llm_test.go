package llm_test

import (
	"context"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/llm"
	"github.com/kunalpednekar/dumpster/internal/llm/mock"
)

// Compile-time checks that mock types satisfy the interfaces.
var _ llm.Embedder = (*mock.Embedder)(nil)
var _ llm.Generator = (*mock.Generator)(nil)

func TestMockEmbedder(t *testing.T) {
	e := mock.NewEmbedder(1536)

	if e.Dims() != 1536 {
		t.Fatalf("dims: got %d, want 1536", e.Dims())
	}

	vecs, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 {
		t.Fatalf("len(vecs): got %d, want 2", len(vecs))
	}
	if len(vecs[0]) != 1536 {
		t.Fatalf("vec dims: got %d, want 1536", len(vecs[0]))
	}
}

func TestMockGenerator(t *testing.T) {
	g := mock.NewGenerator("the answer")
	out, err := g.Generate(context.Background(), "question")
	if err != nil {
		t.Fatal(err)
	}
	if out != "the answer" {
		t.Fatalf("got %q, want %q", out, "the answer")
	}
}

func TestMockGeneratorError(t *testing.T) {
	g := mock.NewErrorGenerator("boom")
	_, err := g.Generate(context.Background(), "question")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
