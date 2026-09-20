// Package worker implements the background job runner that processes queued
// document ingestion-pipeline events (chunking/embedding, entity
// extraction, ...), each handled by its own Handler keyed by queue.JobType.
package worker

import (
	"context"
	"errors"
	"log"
	"sync"
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
	// Concurrency is how many jobs Run processes at once, each dequeued and
	// handled independently. Defaults to 1 (today's serial behavior) when
	// zero or negative.
	//
	// Raising this above 1 used to be unsafe (see the System Architecture
	// page's Inference Service sub-page): concurrent jobs used to mean
	// concurrent warm Python subprocesses (GLiNER, the layout classifier)
	// stacking memory inside this same process. Now that those calls go out
	// over HTTP to a separately-scaled inference service instead, that risk
	// is gone — concurrency here only costs goroutines.
	//
	// Safe because Postgres's SKIP LOCKED dequeue and the
	// jobs (document_id, job_type) uniqueness constraint already guarantee
	// exclusive, non-duplicate processing across concurrent consumers,
	// regardless of whether those consumers are separate goroutines in one
	// process or separate machines.
	Concurrency int
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

// Run starts the worker loop(s), as many as w.cfg.Concurrency (default 1).
// It blocks until ctx is cancelled, then returns ctx.Err() once every loop
// has exited.
func (w *Worker) Run(ctx context.Context) error {
	concurrency := w.cfg.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}

	var wg sync.WaitGroup
	errs := make([]error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = w.runLoop(ctx)
		}(i)
	}
	wg.Wait()

	// Every loop exits for the same reason (ctx cancellation) at shutdown;
	// any one of them represents that reason.
	return errs[0]
}

// runLoop is one independent dequeue-process-repeat loop. With
// Concurrency > 1, multiple runLoop instances run concurrently, each
// dequeuing from the same queue.Consumer.
func (w *Worker) runLoop(ctx context.Context) error {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	// consecutiveDequeueErrs drives dequeueBackoff's growing retry delay; it
	// resets on any outcome other than an error (ErrNoJobs included, since
	// that means the database itself answered fine).
	var consecutiveDequeueErrs int

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		job, err := w.consumer.Dequeue(ctx)
		if errors.Is(err, queue.ErrNoJobs) {
			consecutiveDequeueErrs = 0
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
			wait := dequeueBackoff(consecutiveDequeueErrs, w.cfg.PollInterval)
			consecutiveDequeueErrs++
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		consecutiveDequeueErrs = 0
		w.process(ctx, job, time.Now())
	}
}

// resolutionTimeout bounds the fresh context process derives for recording
// a job's outcome (Ack/Nack/OnFailed) once Handle has returned or the
// no-handler-registered case is detected — see process for why this can't
// just reuse ctx. Generous for one DB write; short enough not to hang
// shutdown indefinitely.
const resolutionTimeout = 10 * time.Second

// dequeueMaxBackoff caps how long runLoop waits between retries after a
// non-ErrNoJobs Dequeue error, regardless of how many consecutive failures
// have happened. Real incident: a managed Postgres provider auto-paused
// the project after its usage quota was hit, and Dequeue errored on every
// attempt for the entire outage. Without a cap on the retry delay, an
// unbounded backoff would eventually make recovery slow to notice; capping
// it at 30s bounds both the database's reconnect-attempt load and this
// loop's log output to something negligible even across a multi-day
// outage, while still noticing recovery promptly.
const dequeueMaxBackoff = 30 * time.Second

// dequeueBackoff returns how long runLoop should wait before its
// (attempt+1)-th consecutive retry after a Dequeue error, doubling from
// base each time and capping at dequeueMaxBackoff. attempt is the number
// of consecutive failures observed so far (0 for the first).
//
// This applies identically whether the underlying cause is a genuine
// outage or something like a paused database recovering on its own
// schedule — the correct behavior ("keep trying, less and less often") is
// the same in both cases, so runLoop doesn't need to tell them apart.
func dequeueBackoff(attempt int, base time.Duration) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if attempt < 0 {
		attempt = 0
	}
	// 2^40 * base already exceeds dequeueMaxBackoff for any realistic base,
	// so clamping the shift here keeps the multiplication below from ever
	// overflowing time.Duration's int64 nanoseconds, regardless of how long
	// an outage runs and how large attempt grows.
	const maxShift = 40
	if attempt > maxShift {
		attempt = maxShift
	}
	if d := base * time.Duration(uint64(1)<<uint(attempt)); d > 0 && d < dequeueMaxBackoff {
		return d
	}
	return dequeueMaxBackoff
}

func (w *Worker) process(ctx context.Context, job *queue.Job, started time.Time) {
	handler, ok := w.handlers[job.Type]

	var handleErr error
	if !ok {
		handleErr = errors.New("worker: no handler for job type " + string(job.Type))
		log.Printf("worker: no handler registered for job %s type %q; nacking", job.ID, job.Type)
	} else {
		handleErr = handler.Handle(ctx, job)
	}

	// From here on, record the outcome on a fresh, short-lived context
	// rather than ctx: ctx is the worker process's shutdown context (see
	// cmd/worker/main.go's signal.NotifyContext), and a job interrupted by
	// a shutdown signal (e.g. mid-deploy) arrives here with ctx already
	// cancelled. Reusing it for Nack/Ack would make those calls fail too —
	// for the exact same reason Handle itself just failed — leaving the
	// job stuck in "processing" with nothing in the DB reflecting the
	// interruption, invisible until the 15-minute stale-job reclaim
	// eventually notices. An independent context lets the outcome actually
	// get recorded even when shutdown is the reason Handle failed.
	resolveCtx, cancel := context.WithTimeout(context.Background(), resolutionTimeout)
	defer cancel()

	if handleErr != nil {
		if ok {
			log.Printf("worker: job %s failed (attempt %d/%d): %v", job.ID, job.Attempts+1, job.MaxAttempts, handleErr)
		}
		deadLettered, nackErr := w.consumer.Nack(resolveCtx, job.ID, handleErr)
		if nackErr != nil {
			log.Printf("worker: nack %s: %v", job.ID, nackErr)
			return
		}
		w.recordResolution(resolveCtx, started, "failure", deadLettered)
		if deadLettered && ok {
			log.Printf("worker: job %s dead-lettered after %d attempts", job.ID, job.MaxAttempts)
			handler.OnFailed(resolveCtx, job)
		}
		return
	}
	if err := w.consumer.Ack(resolveCtx, job.ID); err != nil {
		log.Printf("worker: ack %s: %v", job.ID, err)
		return
	}
	w.recordResolution(resolveCtx, started, "success", false)
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
