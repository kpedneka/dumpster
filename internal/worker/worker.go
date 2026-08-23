// Package worker implements the background job runner that processes queued
// document ingestion-pipeline events (chunking/embedding, entity
// extraction, ...), each handled by its own Handler keyed by queue.JobType.
package worker

import (
	"context"
	"errors"
	"log"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

// Handler processes a single job.
type Handler interface {
	// Handle executes the job. A non-nil return causes the worker to nack the job.
	Handle(ctx context.Context, job *queue.Job) error
	// OnFailed is called when a job is dead-lettered after exhausting retries.
	OnFailed(ctx context.Context, job *queue.Job)
}

// Config configures a Worker.
type Config struct {
	// PollInterval is how long the worker waits when the queue is empty.
	// Defaults to 1 second when zero.
	PollInterval time.Duration
	// Instruments is optional; when nil, job duration/failure metrics are
	// not recorded.
	Instruments *telemetry.Instruments
}

// Worker pulls jobs from a Consumer and dispatches them to the Handler
// registered for the job's Type. This keeps each pipeline stage (document
// indexing, entity extraction, ...) independently re-runnable behind a
// single queue/job-stage seam, without one stage's handler having to know
// about the others.
type Worker struct {
	consumer    queue.Consumer
	handlers    map[queue.JobType]Handler
	cfg         Config
	instruments *telemetry.Instruments
}

// New creates a Worker that reads from consumer and dispatches every job to
// handler. The handler is registered both under queue.JobTypeDocumentIndexing
// and as the fallback for jobs with an empty Type (e.g. rows enqueued before
// job typing existed), so existing single-handler callers keep working
// unmodified. Use RegisterHandler to add handlers for additional job types.
func New(consumer queue.Consumer, handler Handler, cfg Config) *Worker {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = time.Second
	}
	w := &Worker{consumer: consumer, handlers: make(map[queue.JobType]Handler), cfg: cfg, instruments: cfg.Instruments}
	w.handlers[queue.JobTypeDocumentIndexing] = handler
	w.handlers[queue.JobType("")] = handler
	return w
}

// RegisterHandler wires handler to process jobs of the given type. It
// overrides any handler previously registered for that type.
func (w *Worker) RegisterHandler(jobType queue.JobType, handler Handler) {
	w.handlers[jobType] = handler
}

// Run starts the worker loop. It blocks until ctx is cancelled, then returns
// ctx.Err(). On ErrNoJobs the worker sleeps PollInterval before retrying.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		job, err := w.consumer.Dequeue(ctx)
		if errors.Is(err, queue.ErrNoJobs) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("worker: dequeue error: %v", err)
			continue
		}

		w.process(ctx, job, time.Now())
	}
}

func (w *Worker) process(ctx context.Context, job *queue.Job, started time.Time) {
	handler, ok := w.handlers[job.Type]
	if !ok {
		log.Printf("worker: no handler registered for job %s type %q; nacking", job.ID, job.Type)
		deadLettered, nackErr := w.consumer.Nack(ctx, job.ID, errors.New("worker: no handler for job type "+string(job.Type)))
		if nackErr != nil {
			log.Printf("worker: nack %s: %v", job.ID, nackErr)
			return
		}
		w.recordResolution(ctx, started, "failure", deadLettered)
		return
	}

	if err := handler.Handle(ctx, job); err != nil {
		log.Printf("worker: job %s failed (attempt %d/%d): %v", job.ID, job.Attempts+1, job.MaxAttempts, err)
		deadLettered, nackErr := w.consumer.Nack(ctx, job.ID, err)
		if nackErr != nil {
			log.Printf("worker: nack %s: %v", job.ID, nackErr)
			return
		}
		w.recordResolution(ctx, started, "failure", deadLettered)
		if deadLettered {
			log.Printf("worker: job %s dead-lettered after %d attempts", job.ID, job.MaxAttempts)
			handler.OnFailed(ctx, job)
		}
		return
	}
	if err := w.consumer.Ack(ctx, job.ID); err != nil {
		log.Printf("worker: ack %s: %v", job.ID, err)
		return
	}
	w.recordResolution(ctx, started, "success", false)
}

// recordResolution records JobDuration for a job that has just been resolved
// (acked or nacked), and JobFailureTotal when that resolution was a
// dead-letter. It is a no-op when Instruments is nil.
func (w *Worker) recordResolution(ctx context.Context, started time.Time, outcome string, deadLettered bool) {
	if w.instruments == nil {
		return
	}
	w.instruments.JobDuration.Record(ctx, float64(time.Since(started).Microseconds())/1000,
		metric.WithAttributes(attribute.String("outcome", outcome)),
	)
	if deadLettered {
		w.instruments.JobFailureTotal.Add(ctx, 1)
	}
}
