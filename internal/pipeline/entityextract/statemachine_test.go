package entityextract_test

import (
	"testing"

	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/pipeline/asltest"
	"github.com/kunalpednekar/dumpster/internal/pipeline/entityextract"
)

// These tests check the state machine definition (statemachine.asl.json)
// against the Go handler it drives.

func render(t *testing.T) asltest.Machine {
	t.Helper()
	return asltest.Render(t, entityextract.Definition, entityextract.DefinitionVars)
}

// mapState returns the single Map state, failing the test otherwise.
func mapState(t *testing.T, m asltest.Machine) asltest.State {
	t.Helper()
	var found []asltest.State
	for _, s := range m.States {
		if s.Type == "Map" {
			found = append(found, s)
		}
	}
	if len(found) != 1 || found[0].ItemProcessor == nil {
		t.Fatalf("want exactly one Map state with an item processor, found %d", len(found))
	}
	return found[0]
}

func TestDefinition_EveryPlaceholderIsDeclared(t *testing.T) {
	asltest.CheckPlaceholdersDeclared(t, entityextract.Definition, entityextract.DefinitionVars)
}

func TestDefinition_RenderDefinitionFillsEveryPlaceholder(t *testing.T) {
	vars := map[string]string{}
	for _, name := range entityextract.DefinitionVars {
		vars[name] = asltest.Placeholder(name)
	}
	got := asltest.Render(t, entityextract.RenderDefinition(vars), nil)
	if want := render(t); got.StartAt != want.StartAt || len(got.States) != len(want.States) {
		t.Error("RenderDefinition output differs from a direct render")
	}
}

func TestDefinition_TransitionsPointAtRealStatesAndEveryStateIsReachable(t *testing.T) {
	asltest.CheckGraph(t, render(t))
}

func TestDefinition_LambdaStepsMatchTheHandlerAndSendTheRightPayload(t *testing.T) {
	tasks := asltest.LambdaTasks(t, render(t), "lambda_arn")
	asltest.CheckSteps(t, tasks, []string{
		entityextract.StepPlan, entityextract.StepPresignBatch, entityextract.StepPersistBatch,
		entityextract.StepFinalize, entityextract.StepRecordFailure,
	})

	run := asltest.JSONName(t, entityextract.Event{}, "Run")
	batch := asltest.JSONName(t, entityextract.Event{}, "Batch")
	for _, step := range []string{entityextract.StepPlan, entityextract.StepFinalize, entityextract.StepRecordFailure} {
		if got := tasks[step].Parameters[run+".$"]; got != "$" {
			t.Errorf("top-level step %q must send the whole state as %s, got %v", step, run, got)
		}
	}
	for _, step := range []string{entityextract.StepPresignBatch, entityextract.StepPersistBatch} {
		p := tasks[step].Parameters
		if p[run+".$"] != "$."+run || p[batch+".$"] != "$."+batch {
			t.Errorf("Map step %q sends %v, want %s from $.%s and %s from $.%s", step, p, run, run, batch, batch)
		}
	}
}

func TestDefinition_EveryTopLevelStepIsCaughtIntoRecordFailure(t *testing.T) {
	asltest.CheckCaughtInto(t, render(t), "RecordFailure")
}

func TestDefinition_LambdaTasksRetryTransientErrorsOnly(t *testing.T) {
	asltest.CheckRetriesOnly(t, asltest.LambdaTasks(t, render(t), "lambda_arn"), pipeline.TransientErrorName)
}

// The Map must iterate the batches Plan returns, and hand each iteration
// the run fields and batch item in the shape Event decodes.
func TestDefinition_MapIteratesThePlanBatches(t *testing.T) {
	m := render(t)
	plan := asltest.LambdaTasks(t, m, "lambda_arn")[entityextract.StepPlan]
	planPath := "$." + asltest.JSONName(t, entityextract.Input{}, "Plan")
	if plan.ResultPath == nil || *plan.ResultPath != planPath {
		t.Fatalf("plan's ResultPath = %v, want %q", plan.ResultPath, planPath)
	}

	ms := mapState(t, m)
	if want := planPath + "." + asltest.JSONName(t, entityextract.Plan{}, "Batches"); ms.ItemsPath != want {
		t.Errorf("Map ItemsPath = %q, want %q", ms.ItemsPath, want)
	}
	if ms.ItemSelector[asltest.JSONName(t, entityextract.Event{}, "Batch")+".$"] != "$$.Map.Item.Value" {
		t.Errorf("ItemSelector = %v, want the batch item as %q", ms.ItemSelector, "$$.Map.Item.Value")
	}
	run, _ := ms.ItemSelector[asltest.JSONName(t, entityextract.Event{}, "Run")].(map[string]any)
	for _, field := range []string{"Type", "DocumentID", "UserID", "Attempt"} {
		name := asltest.JSONName(t, entityextract.Input{}, field)
		if run[name+".$"] != "$."+name {
			t.Errorf("ItemSelector run.%s = %v, want $.%s", name, run[name+".$"], name)
		}
	}
}

// The Batch job must be started with the job name and URLs PresignBatch
// returns, at the path its result is stored.
func TestDefinition_BatchJobUsesThePresignedURLs(t *testing.T) {
	m := render(t)
	presign := asltest.LambdaTasks(t, m, "lambda_arn")[entityextract.StepPresignBatch]
	if presign.ResultPath == nil || *presign.ResultPath != "$.urls" {
		t.Fatalf("presign_batch's ResultPath = %v, want $.urls", presign.ResultPath)
	}

	var job *asltest.State
	for _, s := range mapState(t, m).ItemProcessor.States {
		if s.Resource == "arn:aws:states:::batch:submitJob.sync" {
			s := s
			job = &s
		}
	}
	if job == nil {
		t.Fatal("no batch:submitJob.sync task in the Map's item processor")
	}
	if want := "$.urls." + asltest.JSONName(t, entityextract.BatchURLs{}, "JobName"); job.Parameters["JobName.$"] != want {
		t.Errorf("JobName.$ = %v, want %q", job.Parameters["JobName.$"], want)
	}
	env := job.Parameters["ContainerOverrides"].(map[string]any)["Environment"].([]any)
	want := map[string]string{
		"CHUNKS_URL": "$.urls." + asltest.JSONName(t, entityextract.BatchURLs{}, "ChunksURL"),
		"RESULT_URL": "$.urls." + asltest.JSONName(t, entityextract.BatchURLs{}, "ResultURL"),
	}
	for _, e := range env {
		kv := e.(map[string]any)
		name, _ := kv["Name"].(string)
		if kv["Value.$"] != want[name] {
			t.Errorf("env %s = %v, want %q", name, kv["Value.$"], want[name])
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Errorf("missing env vars: %v", want)
	}
}
