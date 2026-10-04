package docindex_test

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/pipeline/docindex"
)

// These tests check the state machine definition (statemachine.asl.json)
// against the Go handler it drives, since a mismatch between the two only
// shows up as a failed execution in AWS otherwise. AWS-side validation of
// the definition itself happens when it's deployed.

type aslCatch struct {
	ErrorEquals []string
	ResultPath  *string
	Next        string
}

type aslRetry struct {
	ErrorEquals []string
}

type aslChoice struct {
	Variable string
	Next     string
}

type aslState struct {
	Type       string
	Resource   string
	Parameters map[string]any
	Next       string
	End        bool
	Default    string
	Choices    []aslChoice
	Retry      []aslRetry
	Catch      []aslCatch
}

type aslDefinition struct {
	StartAt string
	States  map[string]aslState
}

func renderDefinition(t *testing.T) aslDefinition {
	t.Helper()
	vars := map[string]string{}
	for _, name := range docindex.DefinitionVars {
		vars[name] = "test-" + name
	}
	rendered := docindex.RenderDefinition(vars)
	if strings.Contains(rendered, "${") {
		t.Fatalf("rendered definition still has an unfilled placeholder:\n%s", rendered)
	}
	var def aslDefinition
	if err := json.Unmarshal([]byte(rendered), &def); err != nil {
		t.Fatalf("definition is not valid JSON: %v", err)
	}
	return def
}

func TestDefinition_EveryPlaceholderIsDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, name := range docindex.DefinitionVars {
		declared[name] = true
	}
	raw := docindex.Definition
	for {
		i := strings.Index(raw, "${")
		if i < 0 {
			break
		}
		j := strings.Index(raw[i:], "}")
		name := raw[i+2 : i+j]
		if !declared[name] {
			t.Errorf("placeholder ${%s} is not in DefinitionVars", name)
		}
		raw = raw[i+j:]
	}
}

func TestDefinition_TransitionsPointAtRealStatesAndEveryStateIsReachable(t *testing.T) {
	def := renderDefinition(t)
	if _, ok := def.States[def.StartAt]; !ok {
		t.Fatalf("StartAt %q is not a state", def.StartAt)
	}

	targets := func(s aslState) []string {
		var out []string
		for _, n := range []string{s.Next, s.Default} {
			if n != "" {
				out = append(out, n)
			}
		}
		for _, c := range s.Choices {
			out = append(out, c.Next)
		}
		for _, c := range s.Catch {
			out = append(out, c.Next)
		}
		return out
	}

	reached := map[string]bool{def.StartAt: true}
	queue := []string{def.StartAt}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, next := range targets(def.States[name]) {
			if _, ok := def.States[next]; !ok {
				t.Errorf("state %q transitions to missing state %q", name, next)
				continue
			}
			if !reached[next] {
				reached[next] = true
				queue = append(queue, next)
			}
		}
	}
	for name := range def.States {
		if !reached[name] {
			t.Errorf("state %q is unreachable", name)
		}
	}
}

// lambdaTasks returns the Task states that invoke the docindex Lambda,
// keyed by the step they send.
func lambdaTasks(t *testing.T, def aslDefinition) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, s := range def.States {
		if s.Type != "Task" || s.Resource != "test-lambda_arn" {
			continue
		}
		step, _ := s.Parameters["step"].(string)
		if step == "" {
			t.Errorf("Lambda task %q sends no step", name)
		}
		if s.Parameters["run.$"] != "$" {
			t.Errorf("Lambda task %q must send the whole state as run, got %v", name, s.Parameters["run.$"])
		}
		out[step] = name
	}
	return out
}

func TestDefinition_LambdaStepsMatchTheHandler(t *testing.T) {
	def := renderDefinition(t)
	var got, want []string
	for step := range lambdaTasks(t, def) {
		got = append(got, step)
	}
	for _, step := range []string{docindex.StepPrepare, docindex.StepFinalize, docindex.StepRecordFailure} {
		want = append(want, step)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Lambda steps in the definition = %v, want the handler's steps %v", got, want)
	}
}

func TestDefinition_EveryWorkStepIsCaughtIntoRecordFailure(t *testing.T) {
	def := renderDefinition(t)
	recordFailure := lambdaTasks(t, def)[docindex.StepRecordFailure]

	for name, s := range def.States {
		if s.Type != "Task" || name == recordFailure {
			continue
		}
		caught := false
		for _, c := range s.Catch {
			if c.Next == recordFailure && len(c.ErrorEquals) == 1 && c.ErrorEquals[0] == "States.ALL" &&
				c.ResultPath != nil && *c.ResultPath == "$.error" {
				caught = true
			}
		}
		if !caught {
			t.Errorf("task %q has no States.ALL catch into %q with ResultPath $.error", name, recordFailure)
		}
	}
}

func TestDefinition_LambdaTasksRetryTransientErrorsOnly(t *testing.T) {
	def := renderDefinition(t)
	for step, name := range lambdaTasks(t, def) {
		s := def.States[name]
		var names []string
		for _, r := range s.Retry {
			names = append(names, r.ErrorEquals...)
		}
		has := map[string]bool{}
		for _, n := range names {
			has[n] = true
		}
		if !has[pipeline.TransientErrorName] {
			t.Errorf("step %q does not retry %s", step, pipeline.TransientErrorName)
		}
		for _, broad := range []string{"States.ALL", "States.TaskFailed"} {
			if has[broad] {
				t.Errorf("step %q retries %s, which would retry deterministic failures too", step, broad)
			}
		}
	}
}

// The Choice that decides whether to run the Batch job must read the
// field Prepare actually returns.
func TestDefinition_ChoiceReadsPreparedEmbedField(t *testing.T) {
	def := renderDefinition(t)
	field, _ := reflect.TypeOf(docindex.Prepared{}).FieldByName("Embed")
	tag := strings.Split(field.Tag.Get("json"), ",")[0]
	want := "$.prepare." + tag

	found := false
	for _, s := range def.States {
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
