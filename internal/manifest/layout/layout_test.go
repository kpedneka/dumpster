package layout

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
)

const fakePDF = "%PDF-1.4 fake"

func TestExtractRegions_EmptyInput_NoCommandRun(t *testing.T) {
	called := false
	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py"})
	ex.runCommand = func(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
		called = true
		return nil, nil
	}

	got, err := ex.ExtractRegions(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("ExtractRegions(nil): got (%v, %v), want (nil, nil)", got, err)
	}
	if called {
		t.Error("runCommand should not be invoked for empty input")
	}
}

func TestExtractRegions_MapsJSONResponse(t *testing.T) {
	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py"})
	ex.runCommand = func(_ context.Context, pythonPath, scriptPath string, stdin []byte) ([]byte, error) {
		if pythonPath != "python3" || scriptPath != "script.py" {
			t.Errorf("runCommand args: got (%q, %q)", pythonPath, scriptPath)
		}
		if len(stdin) == 0 {
			t.Error("expected non-empty stdin")
		}
		resp := `{"regions":[
			{"region_type":"native_text","page_number":1,"bbox":[0,0,1,0.5],"text":"Hello world","image_base64":"","needs_vlm":""},
			{"region_type":"figure","page_number":1,"bbox":[0,0.5,1,1],"text":"","image_base64":"aGVsbG8=","needs_vlm":"describe"}
		]}`
		return []byte(resp), nil
	}

	got, err := ex.ExtractRegions(context.Background(), []byte(fakePDF))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d regions, want 2", len(got))
	}

	text := got[0]
	if text.RegionType != "native_text" || text.Text != "Hello world" || text.NeedsVLM != "" {
		t.Errorf("text region: got %+v", text)
	}
	if text.BoundingBox != [4]float64{0, 0, 1, 0.5} {
		t.Errorf("text bbox: got %v", text.BoundingBox)
	}

	fig := got[1]
	if fig.RegionType != "figure" || fig.NeedsVLM != "describe" || fig.ImageBase64 != "aGVsbG8=" {
		t.Errorf("figure region: got %+v", fig)
	}
}

// TestExtractRegions_LogsPeakRSS covers the v4.6 instrumentation: peak RSS
// reported by a completed run should be logged so future machine-sizing
// decisions can be based on a measured distribution, not a few OOM-kill log
// lines from jobs that crashed.
func TestExtractRegions_LogsPeakRSS(t *testing.T) {
	var buf bytes.Buffer
	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py", Logger: log.New(&buf, "", 0)})
	ex.runCommand = func(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
		return []byte(`{"regions":[],"peak_rss_kb":391272}`), nil
	}

	if _, err := ex.ExtractRegions(context.Background(), []byte(fakePDF)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "391272") {
		t.Errorf("expected log output to report peak RSS, got %q", buf.String())
	}
}

func TestExtractRegions_CommandError_Propagated(t *testing.T) {
	wantErr := errors.New("script exited 1")
	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py"})
	ex.runCommand = func(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
		return nil, wantErr
	}

	_, err := ex.ExtractRegions(context.Background(), []byte(fakePDF))
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestExtractRegions_InvalidJSON_ReturnsError(t *testing.T) {
	ex := New(Config{PythonPath: "python3", ScriptPath: "script.py"})
	ex.runCommand = func(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
		return []byte("not json"), nil
	}

	_, err := ex.ExtractRegions(context.Background(), []byte(fakePDF))
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
