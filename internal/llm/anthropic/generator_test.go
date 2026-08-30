package anthropic

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// roundTripFunc adapts a function to http.RoundTripper, letting tests fake
// the Anthropic API without a real network call or API key — the same
// pattern the SDK's own tests use (see anthropic-sdk-go's
// lib/betafallback/streaming_test.go sseTransport).
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// event formats one SSE frame in the shape the Anthropic Messages streaming
// API sends.
func event(name, data string) string {
	return fmt.Sprintf("event: %s\ndata: %s\n\n", name, data)
}

// fakeGenerator returns a Generator whose every HTTP call is answered with
// the given canned SSE body, regardless of the request.
func fakeGenerator(sse string) *Generator {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sse)),
			Request:    req,
		}, nil
	})
	return New("test-key", "claude-test", option.WithHTTPClient(&http.Client{Transport: transport}))
}

func textDeltaStream(deltas ...string) string {
	var sb strings.Builder
	sb.WriteString(event("message_start", `{"type":"message_start","message":{"type":"message","id":"msg_1","role":"assistant","content":[],"model":"claude-test","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`))
	sb.WriteString(event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	for _, d := range deltas {
		sb.WriteString(event("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, d)))
	}
	sb.WriteString(event("content_block_stop", `{"type":"content_block_stop","index":0}`))
	sb.WriteString(event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`))
	sb.WriteString(event("message_stop", `{"type":"message_stop"}`))
	return sb.String()
}

func TestGenerateStream_DeliversTextDeltasInOrderAndReturnsFullText(t *testing.T) {
	g := fakeGenerator(textDeltaStream("Hello, ", "world."))

	var deltas []string
	full, err := g.GenerateStream(context.Background(), "prompt", func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if full != "Hello, world." {
		t.Errorf("full = %q, want %q", full, "Hello, world.")
	}
	if len(deltas) != 2 || deltas[0] != "Hello, " || deltas[1] != "world." {
		t.Errorf("deltas = %+v, want [\"Hello, \" \"world.\"]", deltas)
	}
}

func TestGenerateStream_EmptyResponse_ReturnsError(t *testing.T) {
	sse := event("message_start", `{"type":"message_start","message":{"type":"message","id":"msg_1","role":"assistant","content":[],"model":"claude-test","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`) +
		event("message_stop", `{"type":"message_stop"}`)
	g := fakeGenerator(sse)

	_, err := g.GenerateStream(context.Background(), "prompt", func(string) {})
	if err == nil {
		t.Fatal("expected error for a stream with no text deltas")
	}
}

func TestGenerateStream_NonOKStatus_ReturnsError(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"api_error","message":"boom"}}`)),
			Request:    req,
		}, nil
	})
	g := New("test-key", "claude-test", option.WithHTTPClient(&http.Client{Transport: transport}))

	_, err := g.GenerateStream(context.Background(), "prompt", func(string) {})
	if err == nil {
		t.Fatal("expected error for a non-200 response")
	}
}

func TestGenerate_Success(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"the answer"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`,
			)),
			Request: req,
		}, nil
	})
	g := New("test-key", "claude-test", option.WithHTTPClient(&http.Client{Transport: transport}))

	out, err := g.Generate(context.Background(), "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if out != "the answer" {
		t.Errorf("got %q, want %q", out, "the answer")
	}
}
