package auth

import "errors"

// ErrDuplicateEmail is returned by UserStore.Create when the email is already taken.
var ErrDuplicateEmail = errors.New("email already registered")
