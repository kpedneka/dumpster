package clerk

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	clerksdk "github.com/clerk/clerk-sdk-go/v2"
	"github.com/clerk/clerk-sdk-go/v2/clerktest"
	clerkjwks "github.com/clerk/clerk-sdk-go/v2/jwks"
	clerkuser "github.com/clerk/clerk-sdk-go/v2/user"
)

// roundTripFunc adapts a function to http.RoundTripper, letting each test
// stub the Clerk Backend API (JWKS + Users endpoints) without a live server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonBody(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// fakeBackendTransport routes JWKS requests to jwksOut and Users requests
// to userOut, mirroring the two Backend API calls Verifier.Verify makes.
func fakeBackendTransport(t *testing.T, jwksOut, userOut json.RawMessage) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/jwks":
			return (&clerktest.RoundTripper{T: t, Out: jwksOut}).RoundTrip(r)
		default:
			return (&clerktest.RoundTripper{T: t, Out: userOut}).RoundTrip(r)
		}
	})
}

func rsaJWK(kid string, pub *rsa.PublicKey) map[string]any {
	return map[string]any{
		"use": "sig",
		"kty": "RSA",
		"kid": kid,
		"alg": "RS256",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}), // 65537
	}
}

func newTestVerifier(t *testing.T, transport http.RoundTripper) *Verifier {
	t.Helper()
	config := &clerksdk.ClientConfig{
		BackendConfig: clerksdk.BackendConfig{HTTPClient: &http.Client{Transport: transport}},
	}
	return &Verifier{
		jwks:  clerkjwks.NewClient(config),
		users: clerkuser.NewClient(config),
	}
}

func TestVerifier_Verify_success(t *testing.T) {
	kid := "test-kid"
	now := time.Now()
	token, pub := clerktest.GenerateJWT(t, map[string]any{
		"iss": "https://clerk.example.accounts.dev",
		"sub": "user_abc123",
		"exp": now.Add(time.Hour).Unix(),
		"iat": now.Unix(),
	}, kid)

	jwksOut, _ := json.Marshal(map[string]any{"keys": []map[string]any{rsaJWK(kid, pub.(*rsa.PublicKey))}})
	userOut := []byte(jsonBody(map[string]any{
		"id":                        "user_abc123",
		"primary_email_address_id": "idn_1",
		"email_addresses": []map[string]any{
			{"id": "idn_1", "email_address": "alice@example.com"},
		},
	}))

	v := newTestVerifier(t, fakeBackendTransport(t, jwksOut, userOut))

	identity, err := v.Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if identity.ClerkUserID != "user_abc123" {
		t.Errorf("ClerkUserID: got %q, want %q", identity.ClerkUserID, "user_abc123")
	}
	if identity.Email != "alice@example.com" {
		t.Errorf("Email: got %q, want %q", identity.Email, "alice@example.com")
	}
}

func TestVerifier_Verify_userLookupFailureIsNonFatal(t *testing.T) {
	kid := "test-kid"
	now := time.Now()
	token, pub := clerktest.GenerateJWT(t, map[string]any{
		"iss": "https://clerk.example.accounts.dev",
		"sub": "user_abc123",
		"exp": now.Add(time.Hour).Unix(),
		"iat": now.Unix(),
	}, kid)

	jwksOut, _ := json.Marshal(map[string]any{"keys": []map[string]any{rsaJWK(kid, pub.(*rsa.PublicKey))}})

	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/jwks" {
			return (&clerktest.RoundTripper{T: t, Out: jwksOut}).RoundTrip(r)
		}
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: http.NoBody, Header: http.Header{}}, nil
	})
	v := newTestVerifier(t, transport)

	identity, err := v.Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("Verify should still succeed when the user lookup fails: %v", err)
	}
	if identity.ClerkUserID != "user_abc123" {
		t.Errorf("ClerkUserID: got %q, want %q", identity.ClerkUserID, "user_abc123")
	}
	if identity.Email != "" {
		t.Errorf("Email: got %q, want empty when lookup fails", identity.Email)
	}
}

func TestVerifier_Verify_invalidToken(t *testing.T) {
	v := newTestVerifier(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("no HTTP call expected for a malformed token")
		return nil, nil
	}))

	if _, err := v.Verify(t.Context(), "not-a-jwt"); err == nil {
		t.Fatal("expected error for malformed token")
	}
}

func TestVerifier_Verify_expiredToken(t *testing.T) {
	kid := "test-kid"
	now := time.Now()
	token, pub := clerktest.GenerateJWT(t, map[string]any{
		"iss": "https://clerk.example.accounts.dev",
		"sub": "user_abc123",
		"exp": now.Add(-time.Hour).Unix(),
		"iat": now.Add(-2 * time.Hour).Unix(),
	}, kid)

	jwksOut, _ := json.Marshal(map[string]any{"keys": []map[string]any{rsaJWK(kid, pub.(*rsa.PublicKey))}})
	v := newTestVerifier(t, fakeBackendTransport(t, jwksOut, nil))

	if _, err := v.Verify(t.Context(), token); err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestVerifier_Verify_invalidIssuer(t *testing.T) {
	kid := "test-kid"
	now := time.Now()
	token, pub := clerktest.GenerateJWT(t, map[string]any{
		"iss": "https://not-clerk.example.com",
		"sub": "user_abc123",
		"exp": now.Add(time.Hour).Unix(),
		"iat": now.Unix(),
	}, kid)

	jwksOut, _ := json.Marshal(map[string]any{"keys": []map[string]any{rsaJWK(kid, pub.(*rsa.PublicKey))}})
	v := newTestVerifier(t, fakeBackendTransport(t, jwksOut, nil))

	if _, err := v.Verify(t.Context(), token); err == nil {
		t.Fatal("expected error for invalid issuer")
	}
}

func TestNew(t *testing.T) {
	v := New("sk_test_xxx")
	if v == nil {
		t.Fatal("New returned nil")
	}
}
