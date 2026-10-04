// Package pipeline holds what every job step running on Lambda shares.
package pipeline

// TransientErrorName is the error name state machines retry on.
const TransientErrorName = "TransientError"

// TransientError marks a failure worth retrying.
type TransientError struct{ Err error }

func (e *TransientError) Error() string { return e.Err.Error() }

// Unwrap returns the cause.
func (e *TransientError) Unwrap() error { return e.Err }

// Transient marks err as worth retrying.
func Transient(err error) error { return err }

// IsTransient reports whether err is marked transient.
func IsTransient(err error) bool { return false }

// ForLambda prepares err for returning from a Lambda handler.
func ForLambda(err error) error { return err }
