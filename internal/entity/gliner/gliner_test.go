package gliner

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/entity"
)

// fakeProcess is a test double for sidecarProcess: each call to send returns
// the next canned response in order, defaulting to an empty-entities
// response once exhausted.
type fakeProcess struct {
	responses []fakeResponse
	calls     int
}

type fakeResponse struct {
	body []byte
	err  error
}

func (f *fakeProcess) send(_ []byte) ([]byte, error) {
	i := f.calls
	f.calls++
	if i >= len(f.responses) {
		return []byte(`{"entities":[]}`), nil
	}
	return f.responses[i].body, f.responses[i].err
}

// newTestExtractor wires ex.startProcess to return proc and increment
// startCount each time it's invoked, so tests can assert how many times the
// sidecar was (re)started.
func newTestExtractor(startCount *int, proc sidecarProcess) *Extractor {
	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py"})
	ex.startProcess = func(_, _ string) (sidecarProcess, error) {
		*startCount++
		return proc, nil
	}
	return ex
}

func TestExtract_EmptyInputs_NoProcessStarted(t *testing.T) {
	startCount := 0
	ex := newTestExtractor(&startCount, &fakeProcess{})

	got, err := ex.Extract(context.Background(), nil, []entity.Type{"person"})
	if err != nil || got != nil {
		t.Fatalf("Extract(no chunks): got (%v, %v), want (nil, nil)", got, err)
	}
	got, err = ex.Extract(context.Background(), []*chunk.Chunk{{Text: "hi"}}, nil)
	if err != nil || got != nil {
		t.Fatalf("Extract(no types): got (%v, %v), want (nil, nil)", got, err)
	}
	if startCount != 0 {
		t.Error("sidecar should not be started when there is nothing to extract")
	}
}

func TestExtract_MapsResponseOntoChunkFields(t *testing.T) {
	docID, kbID, userID, chunkID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	c := &chunk.Chunk{ID: chunkID, DocumentID: docID, KBID: kbID, UserID: userID, Text: "Ada Lovelace wrote notes."}

	resp := `{"entities":[{"chunk_id":"` + chunkID.String() + `","type":"person","text":"Ada Lovelace","start":0,"end":12,"score":0.93}]}`
	startCount := 0
	ex := newTestExtractor(&startCount, &fakeProcess{responses: []fakeResponse{{body: []byte(resp)}}})

	got, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entities, want 1", len(got))
	}
	e := got[0]
	if e.DocumentID != docID || e.KBID != kbID || e.UserID != userID || e.ChunkID != chunkID {
		t.Errorf("entity not populated from source chunk: %+v", e)
	}
	if e.Type != "person" || e.Text != "Ada Lovelace" || e.Start != 0 || e.End != 12 {
		t.Errorf("unexpected entity fields: %+v", e)
	}
	if e.Score != 0.93 {
		t.Errorf("score: got %v, want 0.93", e.Score)
	}
}

func TestExtract_UnknownChunkID_Skipped(t *testing.T) {
	c := &chunk.Chunk{ID: uuid.New(), Text: "hello"}
	resp := `{"entities":[{"chunk_id":"` + uuid.New().String() + `","type":"person","text":"X","start":0,"end":1,"score":0.5}]}`
	startCount := 0
	ex := newTestExtractor(&startCount, &fakeProcess{responses: []fakeResponse{{body: []byte(resp)}}})

	got, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected entities for unknown chunk ids to be skipped, got %d", len(got))
	}
}

func TestExtract_SidecarError_Propagated(t *testing.T) {
	wantErr := errors.New("broken pipe")
	startCount := 0
	ex := newTestExtractor(&startCount, &fakeProcess{responses: []fakeResponse{{err: wantErr}}})

	_, err := ex.Extract(context.Background(), []*chunk.Chunk{{ID: uuid.New(), Text: "hi"}}, []entity.Type{"person"})
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestExtract_InvalidJSON_ReturnsError(t *testing.T) {
	startCount := 0
	ex := newTestExtractor(&startCount, &fakeProcess{responses: []fakeResponse{{body: []byte("not json")}}})

	_, err := ex.Extract(context.Background(), []*chunk.Chunk{{ID: uuid.New(), Text: "hi"}}, []entity.Type{"person"})
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

// TestExtract_ReusesSameProcessAcrossCalls is the core property of this
// card: the sidecar should be started once and reused, not respawned per
// document (which is what made every document pay the ~17.5s model-load
// cost in the first place).
func TestExtract_ReusesSameProcessAcrossCalls(t *testing.T) {
	startCount := 0
	proc := &fakeProcess{}
	ex := newTestExtractor(&startCount, proc)

	for i := 0; i < 3; i++ {
		if _, err := ex.Extract(context.Background(), []*chunk.Chunk{{ID: uuid.New(), Text: "hi"}}, []entity.Type{"person"}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if startCount != 1 {
		t.Errorf("sidecar should be started once and reused, got %d starts", startCount)
	}
	if proc.calls != 3 {
		t.Errorf("expected 3 requests sent to the same process, got %d", proc.calls)
	}
}

// TestExtract_RestartsSidecarAfterCrash: a document that kills the sidecar
// (e.g. a pathological input) is allowed to fail that one call — it goes
// through the normal job-retry path — but must not leave the extractor
// permanently unusable. The next call should transparently get a fresh
// process rather than hanging or erroring against the dead one.
func TestExtract_RestartsSidecarAfterCrash(t *testing.T) {
	firstProc := &fakeProcess{responses: []fakeResponse{{err: errors.New("broken pipe: process exited")}}}
	secondProc := &fakeProcess{}
	procs := []sidecarProcess{firstProc, secondProc}

	startCount := 0
	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py"})
	ex.startProcess = func(_, _ string) (sidecarProcess, error) {
		p := procs[startCount]
		startCount++
		return p, nil
	}

	if _, err := ex.Extract(context.Background(), []*chunk.Chunk{{ID: uuid.New(), Text: "hi"}}, []entity.Type{"person"}); err == nil {
		t.Fatal("expected the first call to surface the sidecar error")
	}
	if _, err := ex.Extract(context.Background(), []*chunk.Chunk{{ID: uuid.New(), Text: "hi"}}, []entity.Type{"person"}); err != nil {
		t.Fatalf("second call should succeed against a freshly restarted sidecar: %v", err)
	}
	if startCount != 2 {
		t.Errorf("expected sidecar to be restarted once after the crash, got %d starts", startCount)
	}
}

func TestNew_DefaultsToRealStartProcess(t *testing.T) {
	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py"})
	if ex.startProcess == nil {
		t.Fatal("expected New to wire a default startProcess")
	}
}

var _ entity.Extractor = (*Extractor)(nil)
