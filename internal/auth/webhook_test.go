package auth_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/auth"
)

const testWebhookSecretRaw = "supersecretvalue1234567890123456"

func testWebhookSecret() string {
	return "whsec_" + base64.StdEncoding.EncodeToString([]byte(testWebhookSecretRaw))
}

func signPayload(t *testing.T, secret, id, timestamp string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(testWebhookSecretRaw))
	mac.Write([]byte(id + "." + timestamp + "." + string(body)))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return "v1," + sig
}

func TestVerifyWebhook_valid(t *testing.T) {
	secret := testWebhookSecret()
	body := []byte(`{"type":"user.created","data":{"id":"user_123"}}`)
	id, ts := "msg_1", "1700000000"
	header := signPayload(t, secret, id, ts, body)

	evt, err := auth.VerifyWebhook(secret, id, ts, body, header)
	if err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}
	if evt.Type != "user.created" {
		t.Errorf("Type: got %q, want %q", evt.Type, "user.created")
	}
}

func TestVerifyWebhook_multipleSignatures(t *testing.T) {
	secret := testWebhookSecret()
	body := []byte(`{"type":"user.created","data":{"id":"user_123"}}`)
	id, ts := "msg_1", "1700000000"
	valid := signPayload(t, secret, id, ts, body)
	header := "v1,bogus== " + valid

	if _, err := auth.VerifyWebhook(secret, id, ts, body, header); err != nil {
		t.Fatalf("VerifyWebhook with multiple signatures: %v", err)
	}
}

func TestVerifyWebhook_wrongSignature(t *testing.T) {
	secret := testWebhookSecret()
	body := []byte(`{"type":"user.created","data":{"id":"user_123"}}`)

	_, err := auth.VerifyWebhook(secret, "msg_1", "1700000000", body, "v1,"+base64.StdEncoding.EncodeToString([]byte("not-the-real-signature-bytes")))
	if err == nil {
		t.Fatal("expected error for wrong signature")
	}
}

func TestVerifyWebhook_tamperedBody(t *testing.T) {
	secret := testWebhookSecret()
	body := []byte(`{"type":"user.created","data":{"id":"user_123"}}`)
	id, ts := "msg_1", "1700000000"
	header := signPayload(t, secret, id, ts, body)

	tampered := []byte(`{"type":"user.deleted","data":{"id":"user_123"}}`)
	if _, err := auth.VerifyWebhook(secret, id, ts, tampered, header); err == nil {
		t.Fatal("expected error for tampered body")
	}
}

func TestVerifyWebhook_malformedSecret(t *testing.T) {
	body := []byte(`{"type":"user.created","data":{"id":"user_123"}}`)
	_, err := auth.VerifyWebhook("whsec_not-valid-base64!!!", "msg_1", "1700000000", body, "v1,abc")
	if err == nil {
		t.Fatal("expected error for malformed secret")
	}
}

func TestVerifyWebhook_emptySignatureHeader(t *testing.T) {
	secret := testWebhookSecret()
	body := []byte(`{"type":"user.created","data":{"id":"user_123"}}`)
	_, err := auth.VerifyWebhook(secret, "msg_1", "1700000000", body, "")
	if err == nil {
		t.Fatal("expected error for empty signature header")
	}
}

func TestWebhookUserData_PrimaryEmail(t *testing.T) {
	d := auth.WebhookUserData{
		PrimaryEmailID: "email_2",
		EmailAddresses: []struct {
			ID           string `json:"id"`
			EmailAddress string `json:"email_address"`
		}{
			{ID: "email_1", EmailAddress: "first@example.com"},
			{ID: "email_2", EmailAddress: "primary@example.com"},
		},
	}
	if got := d.PrimaryEmail(); got != "primary@example.com" {
		t.Errorf("PrimaryEmail: got %q, want %q", got, "primary@example.com")
	}
}

func TestWebhookUserData_PrimaryEmail_fallback(t *testing.T) {
	d := auth.WebhookUserData{
		PrimaryEmailID: "missing",
		EmailAddresses: []struct {
			ID           string `json:"id"`
			EmailAddress string `json:"email_address"`
		}{
			{ID: "email_1", EmailAddress: "only@example.com"},
		},
	}
	if got := d.PrimaryEmail(); got != "only@example.com" {
		t.Errorf("PrimaryEmail fallback: got %q, want %q", got, "only@example.com")
	}
}

func TestWebhookUserData_PrimaryEmail_none(t *testing.T) {
	d := auth.WebhookUserData{}
	if got := d.PrimaryEmail(); got != "" {
		t.Errorf("PrimaryEmail with no addresses: got %q, want empty", got)
	}
}
