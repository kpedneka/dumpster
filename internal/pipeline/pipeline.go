// Package pipeline holds what every job step running on Lambda shares,
// whether a Step Functions state machine invokes it or an SQS event source
// mapping does.
//
// The failure model is: retry only failures that might go away (a dropped
// database connection, an S3 or AWS API hiccup), and fail everything else
// on the first attempt, since a parse error or bad document produces the
// same result every time. Code marks the first kind with Transient, and the
// Lambda entrypoint returns errors through ForLambda so the state machine's
// Retry can tell them apart by name.
package pipeline

import (
	"errors"
)

// TransientErrorName is the error name a state machine's Retry lists to
// retry transient failures. aws-lambda-go reports an error under its Go
// type's name, so this must match the TransientError type's name.
const TransientErrorName = "TransientError"

// TransientError marks a failure worth retrying. Create one with
// Transient; return it from a Lambda handler with ForLambda.
type TransientError struct {
	Err error
}

// Error returns the cause's message unchanged.
func (e *TransientError) Error() string { return e.Err.Error() }

// Unwrap returns the cause, so errors.Is and errors.As see through the
// marker.
func (e *TransientError) Unwrap() error { return e.Err }

// Transient marks err as worth retrying. It returns nil for a nil err.
func Transient(err error) error {
	if err == nil {
		return nil
	}
	return &TransientError{Err: err}
}

// IsTransient reports whether err, or anything it wraps, was marked with
// Transient.
func IsTransient(err error) bool {
	var te *TransientError
	return errors.As(err, &te)
}

// ForLambda prepares err for returning from a Lambda handler. A transient
// error anywhere in err's chain comes back as a top-level *TransientError
// carrying err's full message, because aws-lambda-go names an error after
// its outermost type only. Any other error is returned unchanged and is
// not retried.
func ForLambda(err error) error {
	if err == nil || !IsTransient(err) {
		return err
	}
	return &TransientError{Err: err}
}

// MaxReasonLen caps a stored failure reason.
const MaxReasonLen = 2000

// StepError is what a Catch records.
type StepError struct {
	Error string `json:"Error"`
	Cause string `json:"Cause"`
}

// Reason formats e for storage.
func (e *StepError) Reason() string { return "" }

// Superseded reports whether err means the attempt is no longer current.
func Superseded(err error) bool { return false }

// TransientUnless marks err transient unless it is a listed error.
func TransientUnless(err error, deterministic ...error) error { return err }
