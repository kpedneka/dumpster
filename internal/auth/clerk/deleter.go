package clerk

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	clerksdk "github.com/clerk/clerk-sdk-go/v2"
	clerkuser "github.com/clerk/clerk-sdk-go/v2/user"
)

// Deleter removes a user from Clerk via the Backend API. It is the only
// piece of account.IdentityDeleter's implementation, kept separate from
// Verifier so session verification and account-management concerns stay
// independently testable even though they share the same SDK client
// construction pattern.
type Deleter struct {
	users *clerkuser.Client
}

// NewDeleter returns a Deleter that authenticates against Clerk using
// secretKey (the CLERK_SECRET_KEY value).
func NewDeleter(secretKey string) *Deleter {
	config := &clerksdk.ClientConfig{
		BackendConfig: clerksdk.BackendConfig{Key: &secretKey},
	}
	return &Deleter{users: clerkuser.NewClient(config)}
}

// DeleteUser deletes the Clerk user identified by clerkUserID. A 404 from
// Clerk (the user is already gone, e.g. a retry after a partially-failed
// prior attempt) is treated as success rather than an error, since the
// caller's desired end state — no Clerk user with this ID — already holds.
func (d *Deleter) DeleteUser(ctx context.Context, clerkUserID string) error {
	_, err := d.users.Delete(ctx, clerkUserID)
	if err == nil {
		return nil
	}

	var apiErr *clerksdk.APIErrorResponse
	if errors.As(err, &apiErr) && apiErr.HTTPStatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("clerk: delete user %s: %w", clerkUserID, err)
}
