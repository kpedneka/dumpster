package anthropic

import (
	"context"
	"encoding/json"
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

func TestGenerateStreamCached_DeliversTextDeltasAndReturnsFullText(t *testing.T) {
	g := fakeGenerator(textDeltaStream("Cached ", "answer."))

	var deltas []string
	full, err := g.GenerateStreamCached(context.Background(), "cacheable prefix", "dynamic suffix", func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if full != "Cached answer." {
		t.Errorf("full = %q, want %q", full, "Cached answer.")
	}
	if len(deltas) != 2 || deltas[0] != "Cached " || deltas[1] != "answer." {
		t.Errorf("deltas = %+v, want [\"Cached \" \"answer.\"]", deltas)
	}
}

// TestGenerateStreamCached_MarksOnlyThePrefixAsCacheable is the test that
// actually matters for this method's whole reason to exist: not that
// streaming works (GenerateStream's tests already cover that identically),
// but that the request Anthropic receives puts cache_control on the prefix
// block and not the suffix -- a caller relying on this to bring down real
// dollar cost needs the wire format to be right, not just the plumbing.
func TestGenerateStreamCached_MarksOnlyThePrefixAsCacheable(t *testing.T) {
	var capturedBody []byte
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		capturedBody, _ = io.ReadAll(req.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(textDeltaStream("ok"))),
			Request:    req,
		}, nil
	})
	g := New("test-key", "claude-test", option.WithHTTPClient(&http.Client{Transport: transport}))

	_, err := g.GenerateStreamCached(context.Background(), "the cacheable prefix", "the dynamic suffix", func(string) {})
	if err != nil {
		t.Fatal(err)
	}

	var body struct {
		Messages []struct {
			Content []struct {
				Text         string `json:"text"`
				CacheControl *struct {
					Type string `json:"type"`
				} `json:"cache_control"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal request body: %v (body: %s)", err, capturedBody)
	}
	if len(body.Messages) != 1 || len(body.Messages[0].Content) != 2 {
		t.Fatalf("request shape = %+v, want exactly 1 message with 2 content blocks", body)
	}

	prefixBlock, suffixBlock := body.Messages[0].Content[0], body.Messages[0].Content[1]
	if prefixBlock.Text != "the cacheable prefix" {
		t.Errorf("prefix block text = %q, want %q", prefixBlock.Text, "the cacheable prefix")
	}
	if prefixBlock.CacheControl == nil || prefixBlock.CacheControl.Type != "ephemeral" {
		t.Errorf("prefix block cache_control = %+v, want {Type: ephemeral}", prefixBlock.CacheControl)
	}
	if suffixBlock.Text != "the dynamic suffix" {
		t.Errorf("suffix block text = %q, want %q", suffixBlock.Text, "the dynamic suffix")
	}
	if suffixBlock.CacheControl != nil {
		t.Errorf("suffix block cache_control = %+v, want nil (not cached)", suffixBlock.CacheControl)
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
