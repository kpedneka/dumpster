// Package pipeline holds what every job step running on Lambda shares,
// whether a Step Functions state machine invokes it or an SQS event source
// mapping does.
//
// The failure model is: retry only failures that might go away (a dropped
// database connection, an S3 or AWS API hiccup), and fail everything else
// on the first attempt, since a parse error or bad document produces the
// same result every time. Code marks the first kind with Transient (which
// leaves deterministic database errors unmarked), and the
// Lambda entrypoint returns errors through ForLambda so the state machine's
// Retry can tell them apart by name.
package pipeline

import (
	"errors"

	"github.com/kunalpednekar/dumpster/internal/jobstatus"
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

// Transient marks err as worth retrying. It returns nil for a nil err,
// and returns err unmarked when it is a deterministic database error (see
// deterministicDBError), so callers can wrap every repository error
// without retrying failures that can't change.
func Transient(err error) error {
	if err == nil || deterministicDBError(err) {
		return err
	}
	return &TransientError{Err: err}
}

// sqlStater is implemented by database driver errors that carry a
// SQLSTATE code, such as pgx's *pgconn.PgError. Matching on the method
// keeps the driver out of this package.
type sqlStater interface {
	SQLState() string
}

// deterministicDBError reports whether err, or anything it wraps, is a
// database error whose SQLSTATE class means retrying can't help: a data
// exception (22, e.g. invalid input syntax), an integrity constraint
// violation (23) or a syntax error or access rule violation (42). Every
// other class -- connection failures (08), insufficient resources (53),
// operator intervention (57), system errors (58), serialization failures
// (40) -- stays retryable.
func deterministicDBError(err error) bool {
	var se sqlStater
	if !errors.As(err, &se) {
		return false
	}
	code := se.SQLState()
	if len(code) < 2 {
		return false
	}
	switch code[:2] {
	case "22", "23", "42":
		return true
	}
	return false
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

// MaxReasonLen caps the failure reason a step stores in Postgres; a Batch
// or Lambda Cause can carry a long stack trace.
const MaxReasonLen = 2000

// StepError is what a state's Catch records at $.error: the error name
// (e.g. States.TaskFailed, TransientError) and its cause.
type StepError struct {
	Error string `json:"Error"`
	Cause string `json:"Cause"`
}

// Reason formats e as the failure reason to store, "<Error>: <Cause>",
// capped at MaxReasonLen. A nil e reads as an unknown failure.
func (e *StepError) Reason() string {
	if e == nil {
		return "unknown failure"
	}
	reason := e.Error + ": " + e.Cause
	if len(reason) > MaxReasonLen {
		reason = reason[:MaxReasonLen]
	}
	return reason
}

// Superseded reports whether err, from a jobstatus.Writer call, means the
// step's attempt is no longer the current one or has already finished. A
// step that sees this should stop without changing anything, since a
// newer attempt owns the document now.
func Superseded(err error) bool {
	return errors.Is(err, jobstatus.ErrAttemptSuperseded) || errors.Is(err, jobstatus.ErrNotActive)
}

// TransientUnless marks err transient unless it is one of the given
// deterministic errors, such as a record that no longer exists, which
// would fail the same way on every retry.
func TransientUnless(err error, deterministic ...error) error {
	for _, d := range deterministic {
		if errors.Is(err, d) {
			return err
		}
	}
	return Transient(err)
}
