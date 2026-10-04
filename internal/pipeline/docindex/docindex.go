// Package docindex implements the Lambda steps of the document indexing
// state machine.
package docindex

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/stats"
)

// Steps the state machine invokes.
const (
	StepPrepare       = "prepare"
	StepFinalize      = "finalize"
	StepRecordFailure = "record_failure"
)

// Definition is the state machine definition template.
var Definition = ""

// DefinitionVars are the placeholders in Definition.
var DefinitionVars = []string{}

// RenderDefinition fills Definition's placeholders.
func RenderDefinition(vars map[string]string) string { return Definition }

// Event is the Lambda payload.
type Event struct {
	Step string `json:"step"`
	Run  Input  `json:"run"`
}

// Input is the execution state.
type Input struct {
	Type       queue.JobType `json:"type"`
	DocumentID uuid.UUID     `json:"document_id"`
	UserID     uuid.UUID     `json:"user_id"`
	Attempt    int           `json:"attempt"`
	Prepare    *Prepared     `json:"prepare,omitempty"`
	Error      *StepError    `json:"error,omitempty"`
}

// Prepared is Prepare's output.
type Prepared struct {
	Stale          bool   `json:"stale,omitempty"`
	AlreadyIndexed bool   `json:"already_indexed,omitempty"`
	Embed          bool   `json:"embed"`
	ChunkCount     int    `json:"chunk_count,omitempty"`
	JobName        string `json:"job_name,omitempty"`
	TextsURL       string `json:"texts_url,omitempty"`
	ResultURL      string `json:"result_url,omitempty"`
	InputKey       string `json:"input_key,omitempty"`
	ResultKey      string `json:"result_key,omitempty"`
}

// StepError is what a Catch records.
type StepError struct {
	Error string `json:"Error"`
	Cause string `json:"Cause"`
}

// Deps are the handler's dependencies.
type Deps struct {
	Docs      document.Repository
	Objects   objectstore.ObjectStore
	Chunks    chunk.Repository
	Splitter  chunk.Splitter
	Publisher queue.Publisher
	Status    jobstatus.Writer
	Stats     stats.Repository
}

// Config holds tunables.
type Config struct {
	PresignTTL time.Duration
}

// Handler runs the steps.
type Handler struct {
	deps Deps
	cfg  Config
}

// New returns a Handler.
func New(deps Deps, cfg Config) *Handler { return &Handler{deps: deps, cfg: cfg} }

// Prepare runs the prepare step.
func (h *Handler) Prepare(ctx context.Context, in Input) (Prepared, error) { return Prepared{}, nil }

// Finalize runs the finalize step.
func (h *Handler) Finalize(ctx context.Context, in Input) error { return nil }

// RecordFailure runs the record_failure step.
func (h *Handler) RecordFailure(ctx context.Context, in Input) error { return nil }

// Handle routes an Event to its step.
func (h *Handler) Handle(ctx context.Context, ev Event) (any, error) { return nil, nil }
