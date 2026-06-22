package clerk

import (
	"net/http"
	"testing"

	clerksdk "github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/clerktest"
	clerkuser "github.com/clerk/clerk-sdk-go/v2/user"
)

func newTestDeleter(transport http.RoundTripper) *Deleter {
	config := &clerksdk.ClientConfig{
		BackendConfig: clerksdk.BackendConfig{HTTPClient: &http.Client{Transport: transport}},
	}
	return &Deleter{users: clerkuser.NewClient(config)}
}

func TestDeleter_DeleteUser_success(t *testing.T) {
	out := jsonBody(map[string]any{"id": "user_abc123", "object": "user", "deleted": true})
	rt := &clerktest.RoundTripper{
		T:      t,
		Status: http.StatusOK,
		Method: http.MethodDelete,
		Path:   "/v1/users/user_abc123",
		Out:    []byte(out),
	}
	d := newTestDeleter(rt)

	if err := d.DeleteUser(t.Context(), "user_abc123"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
}

func TestDeleter_DeleteUser_alreadyDeletedIsNotFatal(t *testing.T) {
	out := jsonBody(map[string]any{"errors": []map[string]any{{"code": "resource_not_found", "message": "not found"}}})
	rt := &clerktest.RoundTripper{T: t, Status: http.StatusNotFound, Out: []byte(out)}
	d := newTestDeleter(rt)

	if err := d.DeleteUser(t.Context(), "user_abc123"); err != nil {
		t.Fatalf("DeleteUser should treat a 404 (already gone) as success: %v", err)
	}
}

func TestDeleter_DeleteUser_failure(t *testing.T) {
	out := jsonBody(map[string]any{"errors": []map[string]any{{"code": "internal_error", "message": "boom"}}})
	rt := &clerktest.RoundTripper{T: t, Status: http.StatusInternalServerError, Out: []byte(out)}
	d := newTestDeleter(rt)

	if err := d.DeleteUser(t.Context(), "user_abc123"); err == nil {
		t.Fatal("expected error from a 500 response")
	}
}

func TestNewDeleter(t *testing.T) {
	d := NewDeleter("sk_test_xxx")
	if d == nil {
		t.Fatal("NewDeleter returned nil")
	}
}
