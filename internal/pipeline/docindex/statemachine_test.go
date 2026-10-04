package docindex_test

import (
	"testing"

	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/pipeline/asltest"
	"github.com/kunalpednekar/dumpster/internal/pipeline/docindex"
)

// These tests check the state machine definition (statemachine.asl.json)
// against the Go handler it drives.

var handlerSteps = []string{docindex.StepPrepare, docindex.StepFinalize, docindex.StepRecordFailure}

func TestDefinition_EveryPlaceholderIsDeclared(t *testing.T) {
	asltest.CheckPlaceholdersDeclared(t, docindex.Definition, docindex.DefinitionVars)
}

func TestDefinition_RenderDefinitionFillsEveryPlaceholder(t *testing.T) {
	vars := map[string]string{}
	for _, name := range docindex.DefinitionVars {
		vars[name] = asltest.Placeholder(name)
	}
	rendered := docindex.RenderDefinition(vars)
	if got, want := asltest.Render(t, rendered, nil), asltest.Render(t, docindex.Definition, docindex.DefinitionVars); got.StartAt != want.StartAt || len(got.States) != len(want.States) {
		t.Errorf("RenderDefinition output differs from a direct render")
	}
}

func TestDefinition_TransitionsPointAtRealStatesAndEveryStateIsReachable(t *testing.T) {
	asltest.CheckGraph(t, asltest.Render(t, docindex.Definition, docindex.DefinitionVars))
}

func TestDefinition_LambdaStepsMatchTheHandler(t *testing.T) {
	m := asltest.Render(t, docindex.Definition, docindex.DefinitionVars)
	tasks := asltest.LambdaTasks(t, m, "lambda_arn")
	asltest.CheckSteps(t, tasks, handlerSteps)
	for step, s := range tasks {
		if s.Parameters["run.$"] != "$" {
			t.Errorf("step %q must send the whole state as run, got %v", step, s.Parameters["run.$"])
		}
	}
}

func TestDefinition_EveryWorkStepIsCaughtIntoRecordFailure(t *testing.T) {
	m := asltest.Render(t, docindex.Definition, docindex.DefinitionVars)
	asltest.CheckCaughtInto(t, m, "RecordFailure")
	if _, ok := asltest.LambdaTasks(t, m, "lambda_arn")[docindex.StepRecordFailure]; !ok {
		t.Error("no Lambda task sends the record_failure step")
	}
}

func TestDefinition_LambdaTasksRetryTransientErrorsOnly(t *testing.T) {
	m := asltest.Render(t, docindex.Definition, docindex.DefinitionVars)
	asltest.CheckRetriesOnly(t, asltest.LambdaTasks(t, m, "lambda_arn"), pipeline.TransientErrorName)
}

// The Choice that decides whether to run the Batch job must read the
// field Prepare actually returns, at the path Prepare's result is stored.
func TestDefinition_ChoiceReadsPreparedEmbedField(t *testing.T) {
	m := asltest.Render(t, docindex.Definition, docindex.DefinitionVars)
	prepare := asltest.LambdaTasks(t, m, "lambda_arn")[docindex.StepPrepare]
	resultPath := "$." + asltest.JSONName(t, docindex.Input{}, "Prepare")
	if prepare.ResultPath == nil || *prepare.ResultPath != resultPath {
		t.Fatalf("prepare's ResultPath = %v, want %q", prepare.ResultPath, resultPath)
	}
	want := resultPath + "." + asltest.JSONName(t, docindex.Prepared{}, "Embed")

	found := false
	for _, s := range m.States {
		for _, c := range s.Choices {
			if c.Variable == want {
				found = true
			} else {
				t.Errorf("choice reads %q, want %q", c.Variable, want)
			}
		}
	}
	if !found {
		t.Errorf("no choice reads %q", want)
	}
}
