package mock_test

import (
	"context"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/llm/mock"
)

func TestEmbedder_Dims(t *testing.T) {
	e := mock.NewEmbedder(128)
	if e.Dims() != 128 {
		t.Errorf("dims: got %d, want 128", e.Dims())
	}
}

func TestEmbedder_Embed(t *testing.T) {
	e := mock.NewEmbedder(4)
	vecs, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 {
		t.Fatalf("vectors: got %d, want 2", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 4 {
			t.Errorf("vecs[%d]: got len %d, want 4", i, len(v))
		}
	}
}

func TestGenerator_Generate(t *testing.T) {
	g := mock.NewGenerator("the answer")
	out, err := g.Generate(context.Background(), "some prompt")
	if err != nil {
		t.Fatal(err)
	}
	if out != "the answer" {
		t.Errorf("output: got %q, want %q", out, "the answer")
	}
}

func TestErrorGenerator_Generate(t *testing.T) {
	g := mock.NewErrorGenerator("llm unavailable")
	_, err := g.Generate(context.Background(), "prompt")
	if err == nil {
		t.Fatal("expected error from error generator")
	}
	if err.Error() != "llm unavailable" {
		t.Errorf("error: got %q, want %q", err.Error(), "llm unavailable")
	}
}
