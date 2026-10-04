// Package regionclassify implements the Lambda steps of the region
// classification state machine.
package regionclassify

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/manifest"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/stats"
)

// Steps the state machine invokes.
const (
	StepStageLayout   = "stage_layout"
	StepBuildChunks   = "build_chunks"
	StepFinalize      = "finalize"
	StepRecordFailure = "record_failure"
)

// Definition is the state machine definition template.
var Definition = ""

// DefinitionVars are its placeholders.
var DefinitionVars = []string{}

// RenderDefinition fills the placeholders.
func RenderDefinition(vars map[string]string) string { return Definition }

// Event is the Lambda payload.
type Event struct {
	Step string `json:"step"`
	Run  Input  `json:"run"`
}

// Input is the execution state.
type Input struct {
	Type       queue.JobType       `json:"type"`
	DocumentID uuid.UUID           `json:"document_id"`
	UserID     uuid.UUID           `json:"user_id"`
	Attempt    int                 `json:"attempt"`
	Layout     *Layout             `json:"layout,omitempty"`
	Prepare    *Prepared           `json:"prepare,omitempty"`
	Error      *pipeline.StepError `json:"error,omitempty"`
}

// Layout is StageLayout's result.
type Layout struct {
	Stale          bool   `json:"stale,omitempty"`
	AlreadyIndexed bool   `json:"already_indexed,omitempty"`
	NeedsLayout    bool   `json:"needs_layout"`
	JobName        string `json:"job_name,omitempty"`
	PDFURL         string `json:"pdf_url,omitempty"`
	ResultURL      string `json:"result_url,omitempty"`
	ResultKey      string `json:"result_key,omitempty"`
}

// Prepared is BuildChunks' result.
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

// Deps are the collaborators.
type Deps struct {
	Docs      document.Repository
	Uploads   objectstore.ObjectStore
	Scratch   objectstore.ObjectStore
	Chunks    chunk.Repository
	Manifest  manifest.Repository
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
type Handler struct{}

// New returns a Handler.
func New(deps Deps, cfg Config) *Handler { return &Handler{} }

// StageLayout runs the stage_layout step.
func (h *Handler) StageLayout(ctx context.Context, in Input) (Layout, error) { return Layout{}, nil }

// BuildChunks runs the build_chunks step.
func (h *Handler) BuildChunks(ctx context.Context, in Input) (Prepared, error) {
	return Prepared{}, nil
}

// Finalize runs the finalize step.
func (h *Handler) Finalize(ctx context.Context, in Input) error { return nil }

// RecordFailure runs the record_failure step.
func (h *Handler) RecordFailure(ctx context.Context, in Input) error { return nil }

// Handle routes an Event.
func (h *Handler) Handle(ctx context.Context, ev Event) (any, error) { return nil, nil }
