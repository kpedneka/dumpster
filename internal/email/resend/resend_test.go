package resend

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	resendsdk "github.com/resend/resend-go/v2"

	"github.com/kunalpednekar/dumpster/internal/email"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(string(b))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func newTestSender(transport http.RoundTripper, from string) *Sender {
	return &Sender{
		client: resendsdk.NewCustomClient(&http.Client{Transport: transport}, "re_test_xxx"),
		from:   from,
	}
}

func TestSender_Send_success(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath, gotMethod = r.URL.Path, r.Method
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		return jsonResponse(http.StatusOK, map[string]any{"id": "email_123"}), nil
	})
	s := newTestSender(transport, "noreply@example.com")

	err := s.Send(t.Context(), email.Message{To: "alice@example.com", Subject: "Hi", HTML: "<p>hi</p>"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method: got %q, want POST", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/emails") {
		t.Errorf("path: got %q, want suffix /emails", gotPath)
	}
	if gotBody["from"] != "noreply@example.com" {
		t.Errorf("from: got %v, want noreply@example.com", gotBody["from"])
	}
	if gotBody["subject"] != "Hi" {
		t.Errorf("subject: got %v, want Hi", gotBody["subject"])
	}
}

func TestSender_Send_failure(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnprocessableEntity, map[string]any{
			"statusCode": 422, "name": "validation_error", "message": "invalid `to` field",
		}), nil
	})
	s := newTestSender(transport, "noreply@example.com")

	err := s.Send(t.Context(), email.Message{To: "not-an-email", Subject: "Hi", HTML: "<p>hi</p>"})
	if err == nil {
		t.Fatal("expected error from a 422 response")
	}
}

func TestNew(t *testing.T) {
	s := New("re_test_xxx", "noreply@example.com")
	if s == nil {
		t.Fatal("New returned nil")
	}
}
