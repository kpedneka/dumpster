// Package asltest checks a state machine definition (Amazon States
// Language) against the Go handler it drives. A mismatch between the two
// otherwise only shows up as a failed execution in AWS. It is for tests
// only; AWS's own validation of the definition happens at deploy time.
package asltest

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Catch is one entry in a state's Catch list.
type Catch struct {
	ErrorEquals []string
	ResultPath  *string
	Next        string
}

// Retry is one entry in a state's Retry list.
type Retry struct {
	ErrorEquals []string
}

// Choice is one rule of a Choice state.
type Choice struct {
	Variable string
	Next     string
}

// State is the subset of a state's fields these checks read.
type State struct {
	Type          string
	Resource      string
	Parameters    map[string]any
	ResultPath    *string
	Next          string
	End           bool
	Default       string
	Choices       []Choice
	Retry         []Retry
	Catch         []Catch
	ItemsPath     string
	ItemSelector  map[string]any
	ItemProcessor *Machine
}

// Machine is a state machine, or a Map state's item processor.
type Machine struct {
	StartAt string
	States  map[string]State
}

// Placeholder returns the value Render substitutes for ${name}.
func Placeholder(name string) string { return "test-" + name }

// Render fills every ${name} placeholder in definition for the names in
// vars, using a stand-in value (see Placeholder), and parses the result.
// It fails the test if a placeholder is left unfilled or the result isn't
// valid JSON.
func Render(t *testing.T, definition string, vars []string) Machine {
	t.Helper()
	pairs := make([]string, 0, 2*len(vars))
	for _, name := range vars {
		pairs = append(pairs, "${"+name+"}", Placeholder(name))
	}
	rendered := strings.NewReplacer(pairs...).Replace(definition)
	if strings.Contains(rendered, "${") {
		t.Fatalf("rendered definition still has an unfilled placeholder:\n%s", rendered)
	}
	var m Machine
	if err := json.Unmarshal([]byte(rendered), &m); err != nil {
		t.Fatalf("definition is not valid JSON: %v", err)
	}
	return m
}

// CheckPlaceholdersDeclared fails the test for any ${name} in definition
// that isn't in vars.
func CheckPlaceholdersDeclared(t *testing.T, definition string, vars []string) {
	t.Helper()
	declared := map[string]bool{}
	for _, name := range vars {
		declared[name] = true
	}
	raw := definition
	for {
		i := strings.Index(raw, "${")
		if i < 0 {
			return
		}
		j := strings.Index(raw[i:], "}")
		if j < 0 {
			t.Errorf("unterminated placeholder near %q", raw[i:])
			return
		}
		if name := raw[i+2 : i+j]; !declared[name] {
			t.Errorf("placeholder ${%s} is not declared", name)
		}
		raw = raw[i+j:]
	}
}

// CheckGraph fails the test if any transition points at a missing state,
// any state is unreachable from StartAt, or a non-terminal state has no
// way forward. Map item processors are checked the same way.
func CheckGraph(t *testing.T, m Machine) {
	t.Helper()
	checkGraph(t, m, "")
}

func checkGraph(t *testing.T, m Machine, scope string) {
	t.Helper()
	if _, ok := m.States[m.StartAt]; !ok {
		t.Errorf("%sStartAt %q is not a state", scope, m.StartAt)
		return
	}
	reached := map[string]bool{m.StartAt: true}
	pending := []string{m.StartAt}
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		s := m.States[name]
		next := transitions(s)
		terminal := s.End || s.Type == "Fail" || s.Type == "Succeed"
		if len(next) == 0 && !terminal {
			t.Errorf("%sstate %q has no transition and isn't terminal", scope, name)
		}
		for _, n := range next {
			if _, ok := m.States[n]; !ok {
				t.Errorf("%sstate %q transitions to missing state %q", scope, name, n)
				continue
			}
			if !reached[n] {
				reached[n] = true
				pending = append(pending, n)
			}
		}
		if s.ItemProcessor != nil {
			checkGraph(t, *s.ItemProcessor, scope+name+" > ")
		}
	}
	for name := range m.States {
		if !reached[name] {
			t.Errorf("%sstate %q is unreachable", scope, name)
		}
	}
}

func transitions(s State) []string {
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

// LambdaTasks returns every Task state, at any depth, whose Resource is
// the ${lambdaVar} placeholder, keyed by the "step" its Parameters send.
// It fails the test for a Lambda task that sends no step, or two tasks
// sending the same step.
func LambdaTasks(t *testing.T, m Machine, lambdaVar string) map[string]State {
	t.Helper()
	out := map[string]State{}
	collectLambdaTasks(t, m, Placeholder(lambdaVar), out)
	return out
}

func collectLambdaTasks(t *testing.T, m Machine, resource string, out map[string]State) {
	t.Helper()
	for name, s := range m.States {
		if s.ItemProcessor != nil {
			collectLambdaTasks(t, *s.ItemProcessor, resource, out)
		}
		if s.Type != "Task" || s.Resource != resource {
			continue
		}
		step, _ := s.Parameters["step"].(string)
		if step == "" {
			t.Errorf("Lambda task %q sends no step", name)
			continue
		}
		if _, dup := out[step]; dup {
			t.Errorf("step %q is sent by more than one Lambda task", step)
		}
		out[step] = s
	}
}

// CheckSteps fails the test unless the Lambda tasks' steps are exactly
// want, the steps the handler accepts.
func CheckSteps(t *testing.T, tasks map[string]State, want []string) {
	t.Helper()
	var got []string
	for step := range tasks {
		got = append(got, step)
	}
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Lambda steps in the definition = %v, want the handler's steps %v", got, want)
	}
}

// CheckCaughtInto fails the test unless every top-level Task and Map
// state, other than target itself, catches States.ALL into target with
// ResultPath $.error. States inside a Map's item processor are covered by
// the Map's own Catch.
func CheckCaughtInto(t *testing.T, m Machine, target string) {
	t.Helper()
	for name, s := range m.States {
		if (s.Type != "Task" && s.Type != "Map") || name == target {
			continue
		}
		caught := false
		for _, c := range s.Catch {
			if c.Next == target && len(c.ErrorEquals) == 1 && c.ErrorEquals[0] == "States.ALL" &&
				c.ResultPath != nil && *c.ResultPath == "$.error" {
				caught = true
			}
		}
		if !caught {
			t.Errorf("state %q has no States.ALL catch into %q with ResultPath $.error", name, target)
		}
	}
}

// CheckRetriesOnly fails the test unless every task in tasks retries
// errorName, and none retries a catch-all (States.ALL or
// States.TaskFailed) that would also retry deterministic failures.
func CheckRetriesOnly(t *testing.T, tasks map[string]State, errorName string) {
	t.Helper()
	for step, s := range tasks {
		has := map[string]bool{}
		for _, r := range s.Retry {
			for _, n := range r.ErrorEquals {
				has[n] = true
			}
		}
		if !has[errorName] {
			t.Errorf("step %q does not retry %s", step, errorName)
		}
		for _, broad := range []string{"States.ALL", "States.TaskFailed"} {
			if has[broad] {
				t.Errorf("step %q retries %s, which would retry deterministic failures too", step, broad)
			}
		}
	}
}

// JSONName returns the JSON key a struct field serializes to, so a test
// can check that a JSONPath in the definition matches the Go type.
func JSONName(t *testing.T, v any, field string) string {
	t.Helper()
	f, ok := reflect.TypeOf(v).FieldByName(field)
	if !ok {
		t.Fatalf("%T has no field %s", v, field)
	}
	return strings.Split(f.Tag.Get("json"), ",")[0]
}
