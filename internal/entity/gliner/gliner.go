// Package gliner is the vendor adapter for local entity extraction. It is
// the only place a dependency on the spaCy+GLiNER Python stack may be
// touched: spaCy and GLiNER have no Go bindings, so this adapter shells out
// to a small local Python script (scripts/extract_entities.py) via
// os/exec, passing chunk text and the allowed entity type set as JSON on
// stdin and reading the extracted entities back as JSON on stdout.
//
// Running extraction locally (vs. an LLM call per document) is the entire
// point of this card: it converts a variable per-document API cost into a
// fixed, sunk hardware cost. Retrieval quality is not a goal here — the
// output is raw material for a future graph-based retrieval feature.
//
// The Python process is a long-lived sidecar, started once and reused
// across many Extract calls, rather than spawned fresh per call (see v4.7):
// spaCy+GLiNER's model load costs ~17.5s regardless of document size, and
// spawning fresh every document meant every document paid that cost. See
// startSidecarProcess for why the process's lifetime is deliberately not
// tied to any single Extract call's context.
package gliner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"

	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/entity"
)

// Config configures an Extractor.
type Config struct {
	// PythonPath is the interpreter used to run ScriptPath (e.g. "python3"
	// or the path to a dedicated virtualenv's interpreter).
	PythonPath string
	// ScriptPath is the path to extract_entities.py.
	ScriptPath string
	// Logger receives sidecar lifecycle events (start, restart after a
	// crash). Defaults to the standard logger when nil.
	Logger *log.Logger
}

// sidecarProcess abstracts the persistent Python process so tests can
// simulate a crash mid-request without a real Python environment.
type sidecarProcess interface {
	// send writes one JSON request and returns the next JSON response.
	// Returns an error if the process has died (e.g. a broken pipe or a
	// closed stdout).
	send(req []byte) ([]byte, error)
}

// Extractor implements entity.Extractor by keeping one long-lived
// spaCy+GLiNER Python process alive across many Extract calls.
type Extractor struct {
	cfg Config

	mu   sync.Mutex
	proc sidecarProcess
	// startProcess is overridable in tests so the os/exec boundary itself
	// (this package) can be exercised without a real Python environment.
	startProcess func(pythonPath, scriptPath string) (sidecarProcess, error)
}

// New returns an Extractor. The sidecar process is not started until the
// first Extract call (lazy), so a worker instance that never needs entity
// extraction never pays the model-load cost at all.
func New(cfg Config) *Extractor {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	return &Extractor{cfg: cfg, startProcess: startSidecarProcess}
}

type request struct {
	AllowedTypes []string       `json:"allowed_types"`
	Chunks       []chunkRequest `json:"chunks"`
}

type chunkRequest struct {
	ChunkID string `json:"chunk_id"`
	Text    string `json:"text"`
}

type response struct {
	Entities []entityResponse `json:"entities"`
}

type entityResponse struct {
	ChunkID string  `json:"chunk_id"`
	Type    string  `json:"type"`
	Text    string  `json:"text"`
	Start   int     `json:"start"`
	End     int     `json:"end"`
	Score   float32 `json:"score"`
}

// Extract runs the local extraction script over chunks, restricted to
// allowedTypes, and maps the result back onto entity.Entity rows populated
// with each input chunk's DocumentID/KBID/UserID.
func (e *Extractor) Extract(ctx context.Context, chunks []*chunk.Chunk, allowedTypes []entity.Type) ([]*entity.Entity, error) {
	if len(chunks) == 0 || len(allowedTypes) == 0 {
		return nil, nil
	}

	byID := make(map[string]*chunk.Chunk, len(chunks))
	req := request{
		AllowedTypes: make([]string, len(allowedTypes)),
		Chunks:       make([]chunkRequest, len(chunks)),
	}
	for i, t := range allowedTypes {
		req.AllowedTypes[i] = string(t)
	}
	for i, c := range chunks {
		id := c.ID.String()
		byID[id] = c
		req.Chunks[i] = chunkRequest{ChunkID: id, Text: c.Text}
	}

	stdin, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("gliner: marshal request: %w", err)
	}

	stdout, err := e.send(stdin)
	if err != nil {
		return nil, fmt.Errorf("gliner: sidecar request: %w", err)
	}

	var resp response
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return nil, fmt.Errorf("gliner: unmarshal response: %w", err)
	}

	out := make([]*entity.Entity, 0, len(resp.Entities))
	for _, re := range resp.Entities {
		c, ok := byID[re.ChunkID]
		if !ok {
			// The script returned an entity for a chunk we didn't send;
			// skip rather than fail the whole batch.
			continue
		}
		out = append(out, &entity.Entity{
			DocumentID: c.DocumentID,
			KBID:       c.KBID,
			UserID:     c.UserID,
			ChunkID:    c.ID,
			Type:       entity.Type(re.Type),
			Text:       re.Text,
			Start:      re.Start,
			End:        re.End,
			Score:      re.Score,
		})
	}
	return out, nil
}

// send delivers req to the persistent sidecar, starting it on first use and
// transparently restarting it if a previous call found it had died. A
// failed send here still returns an error for that call — the caller's job
// goes through the normal retry/dead-letter path — but clears e.proc so the
// next call gets a fresh process instead of hitting the same broken one.
func (e *Extractor) send(req []byte) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.proc == nil {
		e.cfg.Logger.Printf("gliner: starting entity-extraction sidecar")
		proc, err := e.startProcess(e.cfg.PythonPath, e.cfg.ScriptPath)
		if err != nil {
			return nil, fmt.Errorf("start sidecar: %w", err)
		}
		e.proc = proc
	}

	resp, err := e.proc.send(req)
	if err != nil {
		e.cfg.Logger.Printf("gliner: sidecar request failed, will restart on next call: %v", err)
		e.proc = nil
		return nil, err
	}
	return resp, nil
}

// startSidecarProcess starts extract_entities.py as a long-lived process.
//
// Deliberately exec.Command, not exec.CommandContext: this process is
// reused across many Extract calls, each with its own (typically
// short-lived, per-job) context, so tying its lifetime to any single call's
// ctx would kill the sidecar the moment that one call's context ended.
// It shuts down naturally when this Go process exits: the pipe's write end
// closes, extract_entities.py's stdin read loop sees EOF, and it exits on
// its own — no explicit kill needed.
func startSidecarProcess(pythonPath, scriptPath string) (sidecarProcess, error) {
	cmd := exec.Command(pythonPath, scriptPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	// Reap the process in the background to avoid a zombie if it exits
	// unexpectedly; the exit itself is surfaced to callers via send()'s I/O
	// errors, not this goroutine.
	go func() { _ = cmd.Wait() }()

	scanner := bufio.NewScanner(stdoutPipe)
	// A document with many chunks/entities can produce a single response
	// line well beyond bufio.Scanner's 64KB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	return &realSidecarProcess{cmd: cmd, stdin: stdin, stdout: scanner, stderr: &stderr}, nil
}

// realSidecarProcess implements sidecarProcess over a real subprocess's
// stdin/stdout pipes, using newline-delimited JSON: one request line in,
// one response line out per send call.
type realSidecarProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	stderr *bytes.Buffer
}

func (p *realSidecarProcess) send(req []byte) ([]byte, error) {
	if _, err := p.stdin.Write(req); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}
	if _, err := p.stdin.Write([]byte("\n")); err != nil {
		return nil, fmt.Errorf("write request newline: %w", err)
	}
	if !p.stdout.Scan() {
		if err := p.stdout.Err(); err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}
		return nil, fmt.Errorf("read response: sidecar exited: %s", p.stderr.String())
	}
	line := p.stdout.Bytes()
	resp := make([]byte, len(line))
	copy(resp, line)
	return resp, nil
}

var _ entity.Extractor = (*Extractor)(nil)
