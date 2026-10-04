package regionclassify_test

import (
	"testing"

	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/pipeline/asltest"
	"github.com/kunalpednekar/dumpster/internal/pipeline/regionclassify"
)

// These tests check the state machine definition (statemachine.asl.json)
// against the Go handler it drives.

func render(t *testing.T) asltest.Machine {
	t.Helper()
	return asltest.Render(t, regionclassify.Definition, regionclassify.DefinitionVars)
}

func TestDefinition_EveryPlaceholderIsDeclared(t *testing.T) {
	asltest.CheckPlaceholdersDeclared(t, regionclassify.Definition, regionclassify.DefinitionVars)
}

func TestDefinition_RenderDefinitionFillsEveryPlaceholder(t *testing.T) {
	vars := map[string]string{}
	for _, name := range regionclassify.DefinitionVars {
		vars[name] = asltest.Placeholder(name)
	}
	got := asltest.Render(t, regionclassify.RenderDefinition(vars), nil)
	if want := render(t); got.StartAt != want.StartAt || len(got.States) != len(want.States) {
		t.Error("RenderDefinition output differs from a direct render")
	}
}

func TestDefinition_TransitionsPointAtRealStatesAndEveryStateIsReachable(t *testing.T) {
	asltest.CheckGraph(t, render(t))
}

func TestDefinition_LambdaStepsMatchTheHandler(t *testing.T) {
	tasks := asltest.LambdaTasks(t, render(t), "lambda_arn")
	asltest.CheckSteps(t, tasks, []string{
		regionclassify.StepStageLayout, regionclassify.StepBuildChunks,
		regionclassify.StepFinalize, regionclassify.StepRecordFailure,
	})
	for step, s := range tasks {
		if s.Parameters["run.$"] != "$" {
			t.Errorf("step %q must send the whole state as run, got %v", step, s.Parameters["run.$"])
		}
	}
}

func TestDefinition_EveryWorkStepIsCaughtIntoRecordFailure(t *testing.T) {
	asltest.CheckCaughtInto(t, render(t), "RecordFailure")
}

func TestDefinition_LambdaTasksRetryTransientErrorsOnly(t *testing.T) {
	asltest.CheckRetriesOnly(t, asltest.LambdaTasks(t, render(t), "lambda_arn"), pipeline.TransientErrorName)
}

// resultPath checks step's ResultPath is $.<json name of Input.field> and
// returns it.
func resultPath(t *testing.T, m asltest.Machine, step, field string) string {
	t.Helper()
	want := "$." + asltest.JSONName(t, regionclassify.Input{}, field)
	s := asltest.LambdaTasks(t, m, "lambda_arn")[step]
	if s.ResultPath == nil || *s.ResultPath != want {
		t.Fatalf("%s's ResultPath = %v, want %q", step, s.ResultPath, want)
	}
	return want
}

// Each Choice must read the flag its step returns, and each Batch job must
// read its job name and URLs from that step's result.
func TestDefinition_ChoicesAndBatchJobsReadTheStepResults(t *testing.T) {
	m := render(t)
	layout := resultPath(t, m, regionclassify.StepStageLayout, "Layout")
	prepare := resultPath(t, m, regionclassify.StepBuildChunks, "Prepare")

	wantChoices := map[string]bool{
		layout + "." + asltest.JSONName(t, regionclassify.Layout{}, "NeedsLayout"): false,
		prepare + "." + asltest.JSONName(t, regionclassify.Prepared{}, "Embed"):    false,
	}
	jobs := map[string]asltest.State{}
	for name, s := range m.States {
		for _, c := range s.Choices {
			if _, ok := wantChoices[c.Variable]; !ok {
				t.Errorf("choice reads %q, which no step returns", c.Variable)
			}
			wantChoices[c.Variable] = true
		}
		if s.Resource == "arn:aws:states:::batch:submitJob.sync" {
			jobs[name] = s
		}
	}
	for v, seen := range wantChoices {
		if !seen {
			t.Errorf("no choice reads %q", v)
		}
	}

	wantJobs := map[string]map[string]string{
		layout: {
			"JobName.$":  layout + "." + asltest.JSONName(t, regionclassify.Layout{}, "JobName"),
			"PDF_URL":    layout + "." + asltest.JSONName(t, regionclassify.Layout{}, "PDFURL"),
			"RESULT_URL": layout + "." + asltest.JSONName(t, regionclassify.Layout{}, "ResultURL"),
		},
		prepare: {
			"JobName.$":  prepare + "." + asltest.JSONName(t, regionclassify.Prepared{}, "JobName"),
			"TEXTS_URL":  prepare + "." + asltest.JSONName(t, regionclassify.Prepared{}, "TextsURL"),
			"RESULT_URL": prepare + "." + asltest.JSONName(t, regionclassify.Prepared{}, "ResultURL"),
		},
	}
	if len(jobs) != len(wantJobs) {
		t.Fatalf("found %d Batch job states, want %d", len(jobs), len(wantJobs))
	}
	for name, s := range jobs {
		got := map[string]string{"JobName.$": s.Parameters["JobName.$"].(string)}
		env := s.Parameters["ContainerOverrides"].(map[string]any)["Environment"].([]any)
		for _, e := range env {
			kv := e.(map[string]any)
			got[kv["Name"].(string)] = kv["Value.$"].(string)
		}
		matched := false
		for _, want := range wantJobs {
			if equalMaps(got, want) {
				matched = true
			}
		}
		if !matched {
			t.Errorf("Batch job %q parameters %v match neither step's result", name, got)
		}
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
