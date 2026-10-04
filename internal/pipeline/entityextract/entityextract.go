// Package entityextract implements the Lambda steps of the entity
// extraction state machine.
package entityextract

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/canonical"
	"github.com/kunalpednekar/dumpster/internal/chunk"
	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/entity"
	"github.com/kunalpednekar/dumpster/internal/jobstatus"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Steps the state machine invokes.
const (
	StepPlan          = "plan"
	StepPresignBatch  = "presign_batch"
	StepPersistBatch  = "persist_batch"
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
	Step  string `json:"step"`
	Run   Input  `json:"run"`
	Batch *Batch `json:"batch,omitempty"`
}

// Input is the execution state.
type Input struct {
	Type       queue.JobType       `json:"type"`
	DocumentID uuid.UUID           `json:"document_id"`
	UserID     uuid.UUID           `json:"user_id"`
	Attempt    int                 `json:"attempt"`
	Plan       *Plan               `json:"plan,omitempty"`
	Error      *pipeline.StepError `json:"error,omitempty"`
}

// Plan is Plan's result.
type Plan struct {
	Stale    bool    `json:"stale,omitempty"`
	NoChunks bool    `json:"no_chunks,omitempty"`
	Batches  []Batch `json:"batches"`
}

// Batch is one Map item.
type Batch struct {
	Index     int    `json:"index"`
	InputKey  string `json:"input_key"`
	ResultKey string `json:"result_key"`
}

// BatchURLs is PresignBatch's result.
type BatchURLs struct {
	JobName   string `json:"job_name"`
	ChunksURL string `json:"chunks_url"`
	ResultURL string `json:"result_url"`
}

// Deps are the collaborators.
type Deps struct {
	Docs      document.Repository
	Chunks    chunk.Repository
	Entities  entity.Repository
	Canonical canonical.Repository
	Objects   objectstore.ObjectStore
	Publisher queue.Publisher
	Status    jobstatus.Writer
}

// Config holds tunables.
type Config struct {
	AllowedTypes []string
	BatchSize    int
	PresignTTL   time.Duration
}

// Handler runs the steps.
type Handler struct {
	deps Deps
	cfg  Config
}

// New returns a Handler.
func New(deps Deps, cfg Config) *Handler { return &Handler{deps: deps, cfg: cfg} }

// Plan runs the plan step.
func (h *Handler) Plan(ctx context.Context, in Input) (Plan, error) { return Plan{}, nil }

// PresignBatch runs the presign_batch step.
func (h *Handler) PresignBatch(ctx context.Context, in Input, b Batch) (BatchURLs, error) {
	return BatchURLs{}, nil
}

// PersistBatch runs the persist_batch step.
func (h *Handler) PersistBatch(ctx context.Context, in Input, b Batch) error { return nil }

// Finalize runs the finalize step.
func (h *Handler) Finalize(ctx context.Context, in Input) error { return nil }

// RecordFailure runs the record_failure step.
func (h *Handler) RecordFailure(ctx context.Context, in Input) error { return nil }

// Handle routes an Event.
func (h *Handler) Handle(ctx context.Context, ev Event) (any, error) { return nil, nil }
