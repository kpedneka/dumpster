package pipeline_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

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
