package router_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/pipeline/router"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// fakeEvent is shaped like every pipeline package's Event: a step and a
// run carrying the job type.
type fakeEvent struct {
	Step string `json:"step"`
	Run  struct {
		Type    queue.JobType `json:"type"`
		Attempt int           `json:"attempt"`
	} `json:"run"`
}

func payload(jobType queue.JobType, step string) json.RawMessage {
	return json.RawMessage(`{"step":"` + step + `","run":{"type":"` + string(jobType) + `","attempt":2}}`)
}

func TestRoute_SendsEachEventToItsJobTypesHandler(t *testing.T) {
	var got []string
	handler := func(name string) func(context.Context, fakeEvent) (any, error) {
		return func(_ context.Context, ev fakeEvent) (any, error) {
			got = append(got, name+":"+ev.Step)
			return ev.Run.Attempt, nil
		}
	}
	r := router.New().
		Register(queue.JobTypeDocumentIndexing, router.Typed(handler("docindex"))).
		Register(queue.JobTypeEntityExtraction, router.Typed(handler("entityextract")))

	out, err := r.Route(context.Background(), payload(queue.JobTypeEntityExtraction, "plan"))
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if out != 2 {
		t.Errorf("output = %v, want the handler's result", out)
	}
	if _, err := r.Route(context.Background(), payload(queue.JobTypeDocumentIndexing, "finalize")); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(got) != 2 || got[0] != "entityextract:plan" || got[1] != "docindex:finalize" {
		t.Errorf("handled %v, want entityextract:plan then docindex:finalize", got)
	}
}

func TestRoute_ReturnsTheHandlersErrorUnchanged(t *testing.T) {
	boom := errors.New("boom")
	r := router.New().Register(queue.JobTypeDocumentIndexing, router.Typed(func(context.Context, fakeEvent) (any, error) {
		return nil, boom
	}))
	// Returned as-is, so a TransientError keeps its type name for the
	// state machine's Retry.
	if _, err := r.Route(context.Background(), payload(queue.JobTypeDocumentIndexing, "prepare")); err != boom {
		t.Errorf("err = %v, want the handler's error itself", err)
	}
}

func TestRoute_UnknownJobType(t *testing.T) {
	r := router.New()
	if _, err := r.Route(context.Background(), payload(queue.JobTypeCanonicalization, "x")); err == nil {
		t.Error("err = nil, want an error for a job type with no handler")
	}
}

func TestRoute_MalformedPayload(t *testing.T) {
	r := router.New().Register(queue.JobTypeDocumentIndexing, router.Typed(func(context.Context, fakeEvent) (any, error) { return nil, nil }))
	for name, body := range map[string]string{
		"not json":        `nope`,
		"wrong run shape": `{"step":"x","run":"document_indexing"}`,
	} {
		if _, err := r.Route(context.Background(), json.RawMessage(body)); err == nil {
			t.Errorf("%s: err = nil, want an error", name)
		}
	}
}

func TestTyped_DecodeError(t *testing.T) {
	h := router.Typed(func(context.Context, struct {
		Run struct {
			Attempt int `json:"attempt"`
		} `json:"run"`
	}) (any, error) {
		return nil, nil
	})
	if _, err := h(context.Background(), json.RawMessage(`{"run":{"attempt":"two"}}`)); err == nil {
		t.Error("err = nil, want a decode error")
	}
}
