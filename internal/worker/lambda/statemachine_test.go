package lambda_test

import (
	"testing"

	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/pipeline/asltest"
	worklambda "github.com/kunalpednekar/dumpster/internal/worker/lambda"
)

// These tests check the state machine definition (statemachine.asl.json),
// shared by edge extraction and canonicalization, against the StepHandler
// it drives.

var handlerSteps = []string{worklambda.StepRun, worklambda.StepRecordFailure}

func TestDefinition_EveryPlaceholderIsDeclared(t *testing.T) {
	asltest.CheckPlaceholdersDeclared(t, worklambda.Definition, worklambda.DefinitionVars)
}

func TestDefinition_TransitionsPointAtRealStatesAndEveryStateIsReachable(t *testing.T) {
	asltest.CheckGraph(t, asltest.Render(t, worklambda.Definition, worklambda.DefinitionVars))
}

func TestDefinition_LambdaStepsMatchTheHandler(t *testing.T) {
	m := asltest.Render(t, worklambda.Definition, worklambda.DefinitionVars)
	tasks := asltest.LambdaTasks(t, m, "lambda_arn")
	asltest.CheckSteps(t, tasks, handlerSteps)
	for step, s := range tasks {
		if s.Parameters["run.$"] != "$" {
			t.Errorf("step %q must send the whole state as run, got %v", step, s.Parameters["run.$"])
		}
	}
}

func TestDefinition_RunIsCaughtIntoRecordFailure(t *testing.T) {
	m := asltest.Render(t, worklambda.Definition, worklambda.DefinitionVars)
	asltest.CheckCaughtInto(t, m, "RecordFailure")
}

// record_failure reads the Catch's error from the field StepHandler
// decodes it from.
func TestDefinition_CatchStoresTheErrorWhereRecordFailureReadsIt(t *testing.T) {
	if got := asltest.JSONName(t, worklambda.Input{}, "Error"); got != "error" {
		t.Errorf("Input.Error decodes from %q, but the Catch stores it at $.error", got)
	}
}

func TestDefinition_LambdaTasksRetryTransientErrorsOnly(t *testing.T) {
	m := asltest.Render(t, worklambda.Definition, worklambda.DefinitionVars)
	asltest.CheckRetriesOnly(t, asltest.LambdaTasks(t, m, "lambda_arn"), pipeline.TransientErrorName)
}

// A recorded failure must still end the execution as failed, so a
// failed job shows as a failed execution in the console.
func TestDefinition_RecordFailureEndsInAFailState(t *testing.T) {
	m := asltest.Render(t, worklambda.Definition, worklambda.DefinitionVars)
	next := m.States["RecordFailure"].Next
	if s, ok := m.States[next]; !ok || s.Type != "Fail" {
		t.Errorf("RecordFailure transitions to %q, want a Fail state", next)
	}
}
