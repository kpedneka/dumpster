package auth

import "errors"

// ErrUserNotFound is returned by LocalUserStore lookups when no local user
// record matches the given identity.
var ErrUserNotFound = errors.New("user not found")
