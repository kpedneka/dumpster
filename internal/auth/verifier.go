package auth

import "context"

// Identity is the result of successfully verifying a Clerk session token:
// the external Clerk user ID and the best-known email address for that user.
// Email may be empty if the session token doesn't carry it (callers that
// need a reliable email should still consult LocalUserStore after the
// first GetOrCreateByClerkID call populates it from the webhook/API).
type Identity struct {
	ClerkUserID string
	Email       string
}

// SessionVerifier verifies a bearer token issued by Clerk for an active
// session and extracts the caller's identity from it. Implementations
// live in dedicated adapter packages (e.g. internal/auth/clerk) so that
// no vendor SDK is imported here.
type SessionVerifier interface {
	// Verify checks the signature and expiry of token and returns the
	// identity it asserts. Returns an error if the token is missing,
	// malformed, expired, or fails signature verification.
	Verify(ctx context.Context, token string) (Identity, error)
}
