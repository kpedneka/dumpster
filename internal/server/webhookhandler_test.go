package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testWebhookSecretRaw = "supersecretvalue1234567890123456"

func testWebhookSecret() string {
	return "whsec_" + base64.StdEncoding.EncodeToString([]byte(testWebhookSecretRaw))
}

func signWebhook(id, timestamp, body string) string {
	mac := hmac.New(sha256.New, []byte(testWebhookSecretRaw))
	mac.Write([]byte(id + "." + timestamp + "." + body))
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func webhookRequest(body, id, timestamp, signature string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/clerk", strings.NewReader(body))
	if id != "" {
		req.Header.Set("svix-id", id)
	}
	if timestamp != "" {
		req.Header.Set("svix-timestamp", timestamp)
	}
	if signature != "" {
		req.Header.Set("svix-signature", signature)
	}
	return req
}

func TestWebhook_validSignature(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	deps.WebhookSecret = testWebhookSecret()
	router := NewRouter(deps)

	body := `{"type":"user.created","data":{"id":"user_123"}}`
	id, ts := "msg_1", "1700000000"
	sig := signWebhook(id, ts, body)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, webhookRequest(body, id, ts, sig))

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestWebhook_invalidSignature(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	deps.WebhookSecret = testWebhookSecret()
	router := NewRouter(deps)

	body := `{"type":"user.created","data":{"id":"user_123"}}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, webhookRequest(body, "msg_1", "1700000000", "v1,not-the-right-signature=="))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", w.Code)
	}
}

func TestWebhook_missingHeaders(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	deps.WebhookSecret = testWebhookSecret()
	router := NewRouter(deps)

	body := `{"type":"user.created","data":{"id":"user_123"}}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, webhookRequest(body, "", "", ""))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}

func TestWebhook_disabledWhenSecretEmpty(t *testing.T) {
	deps, _, _, _, _ := defaultDeps()
	deps.WebhookSecret = ""
	router := NewRouter(deps)

	body := `{"type":"user.created","data":{"id":"user_123"}}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, webhookRequest(body, "msg_1", "1700000000", "v1,whatever"))

	// Falls through to the authed catch-all mux, which requires a session
	// and rejects this request — confirming the endpoint isn't registered
	// (and isn't silently treated as public) when no secret is configured.
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401 (route should not be public without a secret)", w.Code)
	}
}
