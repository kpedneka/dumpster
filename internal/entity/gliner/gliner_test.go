package gliner

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/entity"
)

func TestExtract_EmptyInputs_NoCommandRun(t *testing.T) {
	called := false
	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py"})
	ex.runCommand = func(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
		called = true
		return []byte(`{"entities":[]}`), nil
	}

	got, err := ex.Extract(context.Background(), nil, []entity.Type{"person"})
	if err != nil || got != nil {
		t.Fatalf("Extract(no chunks): got (%v, %v), want (nil, nil)", got, err)
	}

	got, err = ex.Extract(context.Background(), []*chunk.Chunk{{Text: "hi"}}, nil)
	if err != nil || got != nil {
		t.Fatalf("Extract(no types): got (%v, %v), want (nil, nil)", got, err)
	}

	if called {
		t.Error("runCommand should not be invoked when there is nothing to extract")
	}
}

func TestExtract_MapsResponseOntoChunkFields(t *testing.T) {
	docID, kbID, userID, chunkID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	c := &chunk.Chunk{ID: chunkID, DocumentID: docID, KBID: kbID, UserID: userID, Text: "Ada Lovelace wrote notes."}

	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py"})
	var gotStdin []byte
	ex.runCommand = func(_ context.Context, pythonPath, scriptPath string, stdin []byte) ([]byte, error) {
		if pythonPath != "python3" || scriptPath != "script.py" {
			t.Errorf("runCommand args: got (%q, %q)", pythonPath, scriptPath)
		}
		gotStdin = stdin
		resp := `{"entities":[{"chunk_id":"` + chunkID.String() + `","type":"person","text":"Ada Lovelace","start":0,"end":12,"score":0.93}]}`
		return []byte(resp), nil
	}

	got, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotStdin) == 0 {
		t.Fatal("expected non-empty stdin payload to be sent to the script")
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
	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py"})
	ex.runCommand = func(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
		resp := `{"entities":[{"chunk_id":"` + uuid.New().String() + `","type":"person","text":"X","start":0,"end":1,"score":0.5}]}`
		return []byte(resp), nil
	}

	got, err := ex.Extract(context.Background(), []*chunk.Chunk{c}, []entity.Type{"person"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected entities for unknown chunk ids to be skipped, got %d", len(got))
	}
}

func TestExtract_CommandError_Propagated(t *testing.T) {
	wantErr := errors.New("script exited 1")
	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py"})
	ex.runCommand = func(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
		return nil, wantErr
	}

	_, err := ex.Extract(context.Background(), []*chunk.Chunk{{ID: uuid.New(), Text: "hi"}}, []entity.Type{"person"})
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestExtract_InvalidJSON_ReturnsError(t *testing.T) {
	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py"})
	ex.runCommand = func(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
		return []byte("not json"), nil
	}

	_, err := ex.Extract(context.Background(), []*chunk.Chunk{{ID: uuid.New(), Text: "hi"}}, []entity.Type{"person"})
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestNew_DefaultsToRealRunCommand(t *testing.T) {
	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py"})
	if ex.runCommand == nil {
		t.Fatal("expected New to wire a default runCommand")
	}
}

var _ entity.Extractor = (*Extractor)(nil)
