// Package auth defines the application's authentication seam: the context
// contract every repository relies on (WithUserID/UserIDFromContext), the
// interfaces a session provider must satisfy (SessionVerifier,
// LocalUserStore), and the HTTP middleware/webhook plumbing built on top
// of them. Vendor-specific implementations live in dedicated adapter
// subpackages (e.g. internal/auth/clerk).
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidWebhookSignature is returned when a webhook payload's signature
// does not match any of the expected signatures for the given secret.
var ErrInvalidWebhookSignature = errors.New("auth: invalid webhook signature")

// WebhookEvent is a typed, already-verified Clerk webhook payload.
// Type is the Clerk event name (e.g. "user.created", "user.deleted").
// Data carries the raw "data" object for the caller to unmarshal further;
// this card only needs the user lifecycle events available for a future
// card to act on, so the data envelope is left generic here.
type WebhookEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// WebhookUserData is the subset of a Clerk user.* webhook payload's "data"
// object this app currently cares about.
type WebhookUserData struct {
	ID             string `json:"id"`
	PrimaryEmailID string `json:"primary_email_address_id"`
	EmailAddresses []struct {
		ID           string `json:"id"`
		EmailAddress string `json:"email_address"`
	} `json:"email_addresses"`
}

// PrimaryEmail returns the email address matching PrimaryEmailID, or the
// first known address as a fallback, or "" if none are present.
func (d WebhookUserData) PrimaryEmail() string {
	for _, e := range d.EmailAddresses {
		if e.ID == d.PrimaryEmailID {
			return e.EmailAddress
		}
	}
	if len(d.EmailAddresses) > 0 {
		return d.EmailAddresses[0].EmailAddress
	}
	return ""
}

// VerifyWebhook validates a Clerk (Svix-delivered) webhook request and
// returns the parsed event on success.
//
// Clerk signs webhooks per the Svix/standard-webhooks scheme: the
// signature header carries one or more space-separated "v1,<base64 hmac>"
// values, computed as base64(HMAC-SHA256(secret, "<id>.<timestamp>.<body>")),
// where secret is the base64 payload following the "whsec_" prefix Clerk
// issues. Implemented directly against the stdlib rather than pulling in
// the Svix SDK, since this is the entire surface this app needs from it.
func VerifyWebhook(secret string, id, timestamp string, body []byte, signatureHeader string) (*WebhookEvent, error) {
	if err := verifySvixSignature(secret, id, timestamp, body, signatureHeader); err != nil {
		return nil, err
	}

	var evt WebhookEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		return nil, fmt.Errorf("auth: decode webhook payload: %w", err)
	}
	return &evt, nil
}

func verifySvixSignature(secret, id, timestamp string, body []byte, signatureHeader string) error {
	secretBytes, err := decodeWebhookSecret(secret)
	if err != nil {
		return fmt.Errorf("auth: decode webhook secret: %w", err)
	}

	signedContent := id + "." + timestamp + "." + string(body)
	mac := hmac.New(sha256.New, secretBytes)
	mac.Write([]byte(signedContent))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	for _, candidate := range strings.Fields(signatureHeader) {
		parts := strings.SplitN(candidate, ",", 2)
		if len(parts) != 2 {
			continue
		}
		if hmac.Equal([]byte(parts[1]), []byte(expected)) {
			return nil
		}
	}
	return ErrInvalidWebhookSignature
}

// decodeWebhookSecret strips Clerk's "whsec_" prefix (if present) and
// base64-decodes the remainder, per the Svix secret format.
func decodeWebhookSecret(secret string) ([]byte, error) {
	secret = strings.TrimPrefix(secret, "whsec_")
	return base64.StdEncoding.DecodeString(secret)
}
