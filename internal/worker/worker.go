// Package worker implements the background job runner that processes queued
// document upload events.
package worker

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/kunalpednekar/dumpster/internal/queue"
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
}

// Worker pulls jobs from a Consumer and dispatches them to a Handler.
type Worker struct {
	consumer queue.Consumer
	handler  Handler
	cfg      Config
}

// New creates a Worker that reads from consumer and dispatches to handler.
func New(consumer queue.Consumer, handler Handler, cfg Config) *Worker {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = time.Second
	}
	return &Worker{consumer: consumer, handler: handler, cfg: cfg}
}

// Run starts the worker loop. It blocks until ctx is cancelled, then returns
// ctx.Err(). On ErrNoJobs the worker sleeps PollInterval before retrying.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		job, err := w.consumer.Dequeue(ctx)
		if errors.Is(err, queue.ErrNoJobs) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(w.cfg.PollInterval):
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

		w.process(ctx, job)
	}
}

func (w *Worker) process(ctx context.Context, job *queue.Job) {
	if err := w.handler.Handle(ctx, job); err != nil {
		log.Printf("worker: job %s failed (attempt %d/%d): %v", job.ID, job.Attempts+1, job.MaxAttempts, err)
		deadLettered, nackErr := w.consumer.Nack(ctx, job.ID, err)
		if nackErr != nil {
			log.Printf("worker: nack %s: %v", job.ID, nackErr)
			return
		}
		if deadLettered {
			log.Printf("worker: job %s dead-lettered after %d attempts", job.ID, job.MaxAttempts)
			w.handler.OnFailed(ctx, job)
		}
		return
	}
	if err := w.consumer.Ack(ctx, job.ID); err != nil {
		log.Printf("worker: ack %s: %v", job.ID, err)
	}
}
