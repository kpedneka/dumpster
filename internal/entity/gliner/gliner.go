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
package gliner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

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
}

// Extractor implements entity.Extractor by shelling out to a local
// spaCy+GLiNER Python process once per Extract call.
type Extractor struct {
	cfg Config
	// runCommand is overridable in tests so the os/exec boundary itself
	// (this package) can be exercised without a real Python environment.
	runCommand func(ctx context.Context, pythonPath, scriptPath string, stdin []byte) ([]byte, error)
}

// New returns an Extractor that invokes cfg.PythonPath cfg.ScriptPath for
// every Extract call.
func New(cfg Config) *Extractor {
	return &Extractor{cfg: cfg, runCommand: runPython}
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

	stdout, err := e.runCommand(ctx, e.cfg.PythonPath, e.cfg.ScriptPath, stdin)
	if err != nil {
		return nil, fmt.Errorf("gliner: run extraction script: %w", err)
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

// runPython executes pythonPath scriptPath, writing stdin to the process's
// stdin and returning its stdout.
func runPython(ctx context.Context, pythonPath, scriptPath string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, pythonPath, scriptPath)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

var _ entity.Extractor = (*Extractor)(nil)
