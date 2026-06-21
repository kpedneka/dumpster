// Package clerk is the sole adapter package permitted to import the Clerk
// Go SDK. It implements auth.SessionVerifier by verifying Clerk session
// JWTs against Clerk's published JSON Web Key Set, and exposes a thin
// client for fetching a user's primary email when a session token doesn't
// carry one. Nothing outside this package (and internal/auth/clerk's own
// tests) should import github.com/clerk/clerk-sdk-go/v2 directly.
package clerk

import (
	"context"
	"fmt"

	clerksdk "github.com/clerk/clerk-sdk-go/v2"
	clerkjwks "github.com/clerk/clerk-sdk-go/v2/jwks"
	clerkjwt "github.com/clerk/clerk-sdk-go/v2/jwt"
	clerkuser "github.com/clerk/clerk-sdk-go/v2/user"
	"github.com/kunalpednekar/dumpster/internal/auth"
)

// Verifier implements auth.SessionVerifier using Clerk's session JWT
// verification (signature + expiry against Clerk's JWKS) and the Clerk
// Backend API to resolve the verified user's primary email address.
type Verifier struct {
	jwks  *clerkjwks.Client
	users *clerkuser.Client
}

// New returns a Verifier that authenticates against Clerk using secretKey
// (the CLERK_SECRET_KEY value). A per-instance Backend is constructed
// rather than relying on the SDK's package-level clerk.SetKey, so multiple
// Verifiers (e.g. across tests) never share global state.
func New(secretKey string) *Verifier {
	config := &clerksdk.ClientConfig{
		BackendConfig: clerksdk.BackendConfig{Key: &secretKey},
	}
	return &Verifier{
		jwks:  clerkjwks.NewClient(config),
		users: clerkuser.NewClient(config),
	}
}

// Verify validates token as a Clerk session JWT and returns the identity
// it asserts. The session's subject claim becomes Identity.ClerkUserID;
// the email is resolved via a Backend API call since session JWTs don't
// carry email by default.
func (v *Verifier) Verify(ctx context.Context, token string) (auth.Identity, error) {
	claims, err := clerkjwt.Verify(ctx, &clerkjwt.VerifyParams{
		Token:      token,
		JWKSClient: v.jwks,
	})
	if err != nil {
		return auth.Identity{}, fmt.Errorf("clerk: verify session token: %w", err)
	}

	identity := auth.Identity{ClerkUserID: claims.Subject}

	u, err := v.users.Get(ctx, claims.Subject)
	if err == nil && u != nil {
		identity.Email = primaryEmail(u)
	}
	// A failure to fetch the email is not fatal to verification: the
	// session itself is valid, and LocalUserStore only needs an email on
	// first sight of a given Clerk user. Subsequent webhook-driven syncs
	// (user.created/.updated) are the more reliable source of truth.

	return identity, nil
}

// primaryEmail returns the email address matching u's PrimaryEmailAddressID,
// falling back to the first known address, or "" if the user has none.
func primaryEmail(u *clerksdk.User) string {
	if u.PrimaryEmailAddressID != nil {
		for _, e := range u.EmailAddresses {
			if e != nil && e.ID == *u.PrimaryEmailAddressID {
				return e.EmailAddress
			}
		}
	}
	if len(u.EmailAddresses) > 0 && u.EmailAddresses[0] != nil {
		return u.EmailAddresses[0].EmailAddress
	}
	return ""
}

var _ auth.SessionVerifier = (*Verifier)(nil)
