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

func TestGenerator_GenerateStream_DefaultsToOneDeltaFromGenerateFn(t *testing.T) {
	g := mock.NewGenerator("the answer")

	var deltas []string
	out, err := g.GenerateStream(context.Background(), "some prompt", func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "the answer" {
		t.Errorf("output: got %q, want %q", out, "the answer")
	}
	if len(deltas) != 1 || deltas[0] != "the answer" {
		t.Errorf("deltas: got %+v, want one delta with the full text", deltas)
	}
}

func TestGenerator_GenerateStream_ErrorPropagatesWithNoDelta(t *testing.T) {
	g := mock.NewErrorGenerator("llm unavailable")

	called := false
	_, err := g.GenerateStream(context.Background(), "prompt", func(string) { called = true })
	if err == nil {
		t.Fatal("expected error from error generator")
	}
	if called {
		t.Error("onDelta must not be called when generation fails")
	}
}

func TestGenerator_GenerateStream_UsesOverrideWhenSet(t *testing.T) {
	g := mock.NewGenerator("unused")
	g.GenerateStreamFn = func(_ context.Context, _ string, onDelta func(string)) (string, error) {
		onDelta("chunk one ")
		onDelta("chunk two")
		return "chunk one chunk two", nil
	}

	var deltas []string
	out, err := g.GenerateStream(context.Background(), "prompt", func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "chunk one chunk two" {
		t.Errorf("output: got %q", out)
	}
	if len(deltas) != 2 || deltas[0] != "chunk one " || deltas[1] != "chunk two" {
		t.Errorf("deltas: got %+v", deltas)
	}
}
