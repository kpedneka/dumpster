package pipeline_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
)

// lambdaErrorType mirrors how aws-lambda-go names a returned error (its
// top-level Go type name), which is what a state machine's Retry matches.
func lambdaErrorType(err error) string {
	t := reflect.TypeOf(err)
	if t.Kind() == reflect.Pointer {
		return t.Elem().Name()
	}
	return t.Name()
}

func TestTransient_IsDetectedThroughWrapping(t *testing.T) {
	cause := errors.New("connection reset")
	err := fmt.Errorf("finalize: list chunks: %w", pipeline.Transient(cause))

	if !pipeline.IsTransient(err) {
		t.Error("IsTransient = false, want true for a wrapped transient error")
	}
	if !errors.Is(err, cause) {
		t.Error("the transient wrapper must keep the cause reachable with errors.Is")
	}
}

func TestTransient_Nil(t *testing.T) {
	if pipeline.Transient(nil) != nil {
		t.Error("Transient(nil) should be nil")
	}
}

func TestIsTransient_PlainError(t *testing.T) {
	if pipeline.IsTransient(errors.New("unparsable document")) {
		t.Error("IsTransient = true, want false for an unmarked error")
	}
}

func TestForLambda_TransientSurfacesAsTransientError(t *testing.T) {
	err := pipeline.ForLambda(fmt.Errorf("prepare: %w", pipeline.Transient(errors.New("timeout"))))

	if got := lambdaErrorType(err); got != pipeline.TransientErrorName {
		t.Errorf("Lambda error type = %q, want %q", got, pipeline.TransientErrorName)
	}
	if want := "prepare: timeout"; err.Error() != want {
		t.Errorf("message = %q, want the full wrapped message %q", err.Error(), want)
	}
}

func TestForLambda_DeterministicErrorIsNotRetryable(t *testing.T) {
	cause := fmt.Errorf("prepare: %w", errors.New("unparsable document"))
	err := pipeline.ForLambda(cause)

	if err != cause {
		t.Errorf("ForLambda changed a non-transient error: got %v", err)
	}
	if got := lambdaErrorType(err); got == pipeline.TransientErrorName {
		t.Errorf("Lambda error type = %q for a deterministic failure", got)
	}
}

func TestForLambda_Nil(t *testing.T) {
	if pipeline.ForLambda(nil) != nil {
		t.Error("ForLambda(nil) should be nil")
	}
}

func TestSuperseded(t *testing.T) {
	cases := map[error]bool{
		jobstatus.ErrAttemptSuperseded:                            true,
		fmt.Errorf("mark processing: %w", jobstatus.ErrNotActive): true,
		jobstatus.ErrNotFound:                                     false,
		errors.New("connection reset"):                            false,
		nil:                                                       false,
	}
	for err, want := range cases {
		if got := pipeline.Superseded(err); got != want {
			t.Errorf("Superseded(%v) = %v, want %v", err, got, want)
		}
	}
}

func TestTransientUnless(t *testing.T) {
	gone := errors.New("not found")
	if err := pipeline.TransientUnless(fmt.Errorf("get: %w", gone), gone); pipeline.IsTransient(err) {
		t.Error("a listed deterministic error must not be marked transient")
	}
	if err := pipeline.TransientUnless(errors.New("timeout"), gone); !pipeline.IsTransient(err) {
		t.Error("any other error must be marked transient")
	}
}

func TestStepError_Reason(t *testing.T) {
	var none *pipeline.StepError
	if got := none.Reason(); got != "unknown failure" {
		t.Errorf("nil Reason() = %q", got)
	}
	e := &pipeline.StepError{Error: "States.TaskFailed", Cause: strings.Repeat("x", 5000)}
	got := e.Reason()
	if !strings.HasPrefix(got, "States.TaskFailed: x") || len(got) != pipeline.MaxReasonLen {
		t.Errorf("Reason() is %d chars, want %d starting with the error name", len(got), pipeline.MaxReasonLen)
	}
}

// dbError stands in for a database driver error that carries a SQLSTATE
// code, the way pgx's *pgconn.PgError does.
type dbError struct{ code string }

func (e *dbError) Error() string    { return "database error " + e.code }
func (e *dbError) SQLState() string { return e.code }

func TestTransient_DeterministicDatabaseErrorsAreNotRetried(t *testing.T) {
	cases := map[string]string{
		"invalid text representation (22P02)": "22P02",
		"numeric value out of range (22003)":  "22003",
		"unique violation (23505)":            "23505",
		"foreign key violation (23503)":       "23503",
		"undefined column (42703)":            "42703",
		"insufficient privilege (42501)":      "42501",
	}
	for name, code := range cases {
		t.Run(name, func(t *testing.T) {
			cause := fmt.Errorf("chunk: bulk create: %w", &dbError{code: code})
			err := pipeline.Transient(cause)
			if pipeline.IsTransient(err) {
				t.Errorf("SQLSTATE %s marked transient; it fails the same way on every retry", code)
			}
			if !errors.Is(err, cause) {
				t.Error("the original error should come back unchanged")
			}
		})
	}
}

func TestTransient_ConnectionAndServerDatabaseErrorsAreRetried(t *testing.T) {
	cases := map[string]string{
		"connection failure (08006)":    "08006",
		"too many connections (53300)":  "53300",
		"admin shutdown (57P01)":        "57P01",
		"serialization failure (40001)": "40001",
		"system I/O error (58030)":      "58030",
		"malformed code (single char)":  "2",
		"empty code":                    "",
	}
	for name, code := range cases {
		t.Run(name, func(t *testing.T) {
			err := pipeline.Transient(fmt.Errorf("query: %w", &dbError{code: code}))
			if !pipeline.IsTransient(err) {
				t.Errorf("SQLSTATE %q should stay transient", code)
			}
		})
	}
}

func TestTransientUnless_DeterministicDatabaseErrorIsNotRetried(t *testing.T) {
	err := pipeline.TransientUnless(fmt.Errorf("persist: %w", &dbError{code: "22P02"}), jobstatus.ErrNotFound)
	if pipeline.IsTransient(err) {
		t.Error("a data exception should not be retried")
	}
	if got := lambdaErrorType(pipeline.ForLambda(err)); got == pipeline.TransientErrorName {
		t.Errorf("ForLambda returned %s; the state machine would retry it", got)
	}
}
