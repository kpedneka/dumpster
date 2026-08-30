package worker_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

// stubConsumer is a test double for queue.Consumer. Mutex-guarded so it's
// also safe for the Concurrency > 1 tests, where multiple goroutines call
// Dequeue/Ack/Nack at once — harmless overhead for the Concurrency == 1
// (default) tests that make up most of this file.
type stubConsumer struct {
	mu               sync.Mutex
	jobs             []*queue.Job
	idx              int
	ackedIDs         []uuid.UUID
	nackedIDs        []uuid.UUID
	deadLetterOnNack bool
}

func (c *stubConsumer) Dequeue(_ context.Context) (*queue.Job, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idx >= len(c.jobs) {
		return nil, queue.ErrNoJobs
	}
	j := c.jobs[c.idx]
	c.idx++
	return j, nil
}

func (c *stubConsumer) Ack(_ context.Context, id uuid.UUID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ackedIDs = append(c.ackedIDs, id)
	return nil
}

func (c *stubConsumer) Nack(_ context.Context, id uuid.UUID, _ error) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nackedIDs = append(c.nackedIDs, id)
	return c.deadLetterOnNack, nil
}

func (c *stubConsumer) ackedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.ackedIDs)
}

// stubHandler is a test double for worker.Handler. Also tracks how many
// Handle calls are in flight at once (peakInFlight), so concurrency tests
// can assert real overlap happened, not just that N calls eventually ran.
type stubHandler struct {
	mu           sync.Mutex
	handledJobs  []*queue.Job
	failedJobs   []*queue.Job
	handleErr    error
	handleDelay  time.Duration
	inFlight     int
	peakInFlight int
}

func (h *stubHandler) Handle(_ context.Context, job *queue.Job) error {
	h.mu.Lock()
	h.handledJobs = append(h.handledJobs, job)
	h.inFlight++
	if h.inFlight > h.peakInFlight {
		h.peakInFlight = h.inFlight
	}
	delay := h.handleDelay
	h.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	h.mu.Lock()
	h.inFlight--
	h.mu.Unlock()
	return h.handleErr
}

func (h *stubHandler) OnFailed(_ context.Context, job *queue.Job) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failedJobs = append(h.failedJobs, job)
}

func (h *stubHandler) handledCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.handledJobs)
}

func TestWorker_ProcessesJob(t *testing.T) {
	job := &queue.Job{ID: uuid.New(), DocumentID: uuid.New(), UserID: uuid.New()}
	consumer := &stubConsumer{jobs: []*queue.Job{job}}
	handler := &stubHandler{}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	w := worker.New(consumer, handler, worker.Config{PollInterval: 10 * time.Millisecond})
	_ = w.Run(ctx)

	if len(handler.handledJobs) != 1 {
		t.Fatalf("Handle calls: got %d, want 1", len(handler.handledJobs))
	}
	if len(consumer.ackedIDs) != 1 || consumer.ackedIDs[0] != job.ID {
		t.Errorf("expected Ack(%s), got %v", job.ID, consumer.ackedIDs)
	}
	if len(consumer.nackedIDs) != 0 {
		t.Errorf("Nack should not be called on success")
	}
}

func TestWorker_NacksOnHandlerError(t *testing.T) {
	job := &queue.Job{ID: uuid.New(), DocumentID: uuid.New(), UserID: uuid.New()}
	consumer := &stubConsumer{jobs: []*queue.Job{job}}
	handler := &stubHandler{handleErr: errors.New("processing failed")}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	w := worker.New(consumer, handler, worker.Config{PollInterval: 10 * time.Millisecond})
	_ = w.Run(ctx)

	if len(consumer.nackedIDs) != 1 || consumer.nackedIDs[0] != job.ID {
		t.Errorf("expected Nack(%s), got %v", job.ID, consumer.nackedIDs)
	}
	if len(consumer.ackedIDs) != 0 {
		t.Errorf("Ack should not be called on handler error")
	}
	if len(handler.failedJobs) != 0 {
		t.Errorf("OnFailed should not be called when not dead-lettered")
	}
}

func TestWorker_OnFailedWhenDeadLettered(t *testing.T) {
	job := &queue.Job{ID: uuid.New(), DocumentID: uuid.New(), UserID: uuid.New()}
	consumer := &stubConsumer{jobs: []*queue.Job{job}, deadLetterOnNack: true}
	handler := &stubHandler{handleErr: errors.New("permanent failure")}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	w := worker.New(consumer, handler, worker.Config{PollInterval: 10 * time.Millisecond})
	_ = w.Run(ctx)

	if len(handler.failedJobs) != 1 {
		t.Errorf("OnFailed calls: got %d, want 1", len(handler.failedJobs))
	}
}

func TestWorker_ShutdownOnContextCancel(t *testing.T) {
	consumer := &stubConsumer{} // always returns ErrNoJobs
	handler := &stubHandler{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	w := worker.New(consumer, handler, worker.Config{PollInterval: 10 * time.Millisecond})
	err := w.Run(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run: got %v, want context.Canceled", err)
	}
}

func TestWorker_DispatchesByJobType(t *testing.T) {
	indexJob := &queue.Job{ID: uuid.New(), Type: queue.JobTypeDocumentIndexing, DocumentID: uuid.New(), UserID: uuid.New()}
	entityJob := &queue.Job{ID: uuid.New(), Type: queue.JobTypeEntityExtraction, DocumentID: uuid.New(), UserID: uuid.New()}
	consumer := &stubConsumer{jobs: []*queue.Job{indexJob, entityJob}}

	indexHandler := &stubHandler{}
	entityHandler := &stubHandler{}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	w := worker.New(consumer, indexHandler, worker.Config{PollInterval: 10 * time.Millisecond})
	w.RegisterHandler(queue.JobTypeEntityExtraction, entityHandler)
	_ = w.Run(ctx)

	if len(indexHandler.handledJobs) != 1 || indexHandler.handledJobs[0].ID != indexJob.ID {
		t.Errorf("indexHandler handled %v, want [%s]", indexHandler.handledJobs, indexJob.ID)
	}
	if len(entityHandler.handledJobs) != 1 || entityHandler.handledJobs[0].ID != entityJob.ID {
		t.Errorf("entityHandler handled %v, want [%s]", entityHandler.handledJobs, entityJob.ID)
	}
	if len(consumer.ackedIDs) != 2 {
		t.Errorf("acked: got %d, want 2", len(consumer.ackedIDs))
	}
}

func TestWorker_NacksJobWithUnregisteredType(t *testing.T) {
	job := &queue.Job{ID: uuid.New(), Type: "unknown_type", DocumentID: uuid.New(), UserID: uuid.New()}
	consumer := &stubConsumer{jobs: []*queue.Job{job}}
	handler := &stubHandler{}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	w := worker.New(consumer, handler, worker.Config{PollInterval: 10 * time.Millisecond})
	_ = w.Run(ctx)

	if len(handler.handledJobs) != 0 {
		t.Errorf("Handle should not be called for an unregistered job type")
	}
	if len(consumer.nackedIDs) != 1 || consumer.nackedIDs[0] != job.ID {
		t.Errorf("expected Nack(%s) for unregistered job type, got %v", job.ID, consumer.nackedIDs)
	}
}

// mustInstruments returns real OTel instruments and their Prometheus scrape
// handler, so tests can assert on actual exposition output.
func mustInstruments(t *testing.T) (*telemetry.Instruments, http.Handler) {
	t.Helper()
	inst, metricsHandler, err := telemetry.Setup(context.Background())
	if err != nil {
		t.Fatalf("telemetry.Setup: %v", err)
	}
	return inst, metricsHandler
}

func scrapeMetrics(t *testing.T, handler http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w.Body.String()
}

// hasHistogramCount reports whether body contains a Prometheus _count sample
// for the given histogram name and outcome label with the given count. The
// exporter may insert a unit suffix (e.g. "_milliseconds") between name and
// "_count", so that gap is matched loosely.
func hasHistogramCount(body, name, outcome string, count int) bool {
	pattern := fmt.Sprintf(`%s[a-z_]*_count\{[^}]*outcome="%s"[^}]*\}\s+%d`, regexp.QuoteMeta(name), regexp.QuoteMeta(outcome), count)
	return regexp.MustCompile(pattern).MatchString(body)
}

// hasBareCounterSample reports whether body contains a Prometheus sample for
// name with the given value, regardless of what labels are present.
func hasBareCounterSample(body, name string, value int) bool {
	pattern := fmt.Sprintf(`%s\{[^}]*\}\s+%d`, regexp.QuoteMeta(name), value)
	return regexp.MustCompile(pattern).MatchString(body)
}

func TestWorker_RecordsJobDurationOnSuccess(t *testing.T) {
	job := &queue.Job{ID: uuid.New(), DocumentID: uuid.New(), UserID: uuid.New()}
	consumer := &stubConsumer{jobs: []*queue.Job{job}}
	handler := &stubHandler{}
	inst, metricsHandler := mustInstruments(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	w := worker.New(consumer, handler, worker.Config{PollInterval: 10 * time.Millisecond, Instruments: inst})
	_ = w.Run(ctx)

	got := scrapeMetrics(t, metricsHandler)
	if !hasHistogramCount(got, "job_duration_ms", "success", 1) {
		t.Errorf("expected job_duration_ms success sample, got:\n%s", got)
	}
	if hasBareCounterSample(got, "job_failure_total", 1) {
		t.Errorf("job_failure_total should not be recorded on success, got:\n%s", got)
	}
}

func TestWorker_RecordsJobDurationOnRescheduledFailure(t *testing.T) {
	job := &queue.Job{ID: uuid.New(), DocumentID: uuid.New(), UserID: uuid.New()}
	consumer := &stubConsumer{jobs: []*queue.Job{job}} // deadLetterOnNack defaults false
	handler := &stubHandler{handleErr: errors.New("processing failed")}
	inst, metricsHandler := mustInstruments(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	w := worker.New(consumer, handler, worker.Config{PollInterval: 10 * time.Millisecond, Instruments: inst})
	_ = w.Run(ctx)

	got := scrapeMetrics(t, metricsHandler)
	if !hasHistogramCount(got, "job_duration_ms", "failure", 1) {
		t.Errorf("expected job_duration_ms failure sample, got:\n%s", got)
	}
	if hasBareCounterSample(got, "job_failure_total", 1) {
		t.Errorf("job_failure_total should not be recorded on a rescheduled (non-dead-lettered) nack, got:\n%s", got)
	}
}

func TestWorker_RecordsJobFailureOnDeadLetter(t *testing.T) {
	job := &queue.Job{ID: uuid.New(), DocumentID: uuid.New(), UserID: uuid.New()}
	consumer := &stubConsumer{jobs: []*queue.Job{job}, deadLetterOnNack: true}
	handler := &stubHandler{handleErr: errors.New("permanent failure")}
	inst, metricsHandler := mustInstruments(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	w := worker.New(consumer, handler, worker.Config{PollInterval: 10 * time.Millisecond, Instruments: inst})
	_ = w.Run(ctx)

	got := scrapeMetrics(t, metricsHandler)
	if !hasBareCounterSample(got, "job_failure_total", 1) {
		t.Errorf("expected job_failure_total sample, got:\n%s", got)
	}
	if !hasHistogramCount(got, "job_duration_ms", "failure", 1) {
		t.Errorf("expected job_duration_ms failure sample, got:\n%s", got)
	}
}

// TestWorker_DefaultConcurrencyIsSerial is the safety net for every
// existing caller: Concurrency left unset (zero value) must preserve
// today's exact one-job-at-a-time behavior.
func TestWorker_DefaultConcurrencyIsSerial(t *testing.T) {
	jobs := make([]*queue.Job, 3)
	for i := range jobs {
		jobs[i] = &queue.Job{ID: uuid.New(), DocumentID: uuid.New(), UserID: uuid.New()}
	}
	consumer := &stubConsumer{jobs: jobs}
	handler := &stubHandler{handleDelay: 20 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	w := worker.New(consumer, handler, worker.Config{PollInterval: 5 * time.Millisecond})
	_ = w.Run(ctx)

	if handler.handledCount() != 3 {
		t.Fatalf("handled %d jobs, want 3", handler.handledCount())
	}
	if handler.peakInFlight != 1 {
		t.Errorf("default concurrency should process one job at a time, observed peak in-flight %d", handler.peakInFlight)
	}
}

// TestWorker_ConcurrencyProcessesJobsInParallel is the core property of
// in-process worker concurrency: raising Concurrency must result in jobs
// genuinely overlapping in time, not just N calls eventually happening —
// this is why the test measures both peak in-flight count and real
// wall-clock elapsed time, rather than only counting completed jobs.
func TestWorker_ConcurrencyProcessesJobsInParallel(t *testing.T) {
	const n = 5
	const delay = 50 * time.Millisecond

	jobs := make([]*queue.Job, n)
	for i := range jobs {
		jobs[i] = &queue.Job{ID: uuid.New(), DocumentID: uuid.New(), UserID: uuid.New()}
	}
	consumer := &stubConsumer{jobs: jobs}
	handler := &stubHandler{handleDelay: delay}

	// Run() blocks until ctx is done regardless of how fast the work
	// finishes, so the proof of concurrency isn't measuring elapsed time —
	// it's that this deadline is enough time at all. Serial processing of n
	// jobs at `delay` each needs n*delay = 250ms; deliberately give this
	// well under that (150ms: one delay period plus scheduling slack) so a
	// serial implementation would still be mid-batch when time runs out,
	// while true concurrency comfortably finishes all n within it.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	w := worker.New(consumer, handler, worker.Config{PollInterval: 5 * time.Millisecond, Concurrency: n})
	_ = w.Run(ctx)

	if handler.handledCount() != n {
		t.Fatalf("handled %d jobs within 150ms, want %d — serial time for this batch is 250ms, so this deadline only being met is itself the concurrency proof", handler.handledCount(), n)
	}
	if consumer.ackedCount() != n {
		t.Fatalf("acked %d jobs, want %d", consumer.ackedCount(), n)
	}
	if handler.peakInFlight < 2 {
		t.Errorf("expected multiple jobs in flight at once, observed peak in-flight %d", handler.peakInFlight)
	}
}

// TestWorker_ConcurrentJobsAcrossTenants_NoCorrectnessRegression exercises
// concurrent processing of jobs belonging to different tenants (and
// documents) at once, confirming every job is still individually acked
// exactly once with no cross-job interference — the correctness half of
// concurrency's requirements, alongside the timing half above.
func TestWorker_ConcurrentJobsAcrossTenants_NoCorrectnessRegression(t *testing.T) {
	const n = 8
	jobs := make([]*queue.Job, n)
	for i := range jobs {
		// Alternate tenants (UserID) and document types across the batch.
		jobs[i] = &queue.Job{
			ID:         uuid.New(),
			DocumentID: uuid.New(),
			UserID:     uuid.New(),
		}
	}
	consumer := &stubConsumer{jobs: jobs}
	handler := &stubHandler{handleDelay: 5 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	w := worker.New(consumer, handler, worker.Config{PollInterval: 5 * time.Millisecond, Concurrency: 4})
	_ = w.Run(ctx)

	if handler.handledCount() != n {
		t.Fatalf("handled %d jobs, want %d", handler.handledCount(), n)
	}
	if consumer.ackedCount() != n {
		t.Fatalf("acked %d jobs, want %d", consumer.ackedCount(), n)
	}
	seen := make(map[uuid.UUID]int)
	for _, id := range consumer.ackedIDs {
		seen[id]++
	}
	for _, j := range jobs {
		if seen[j.ID] != 1 {
			t.Errorf("job %s acked %d times, want exactly 1", j.ID, seen[j.ID])
		}
	}
}

func TestWorker_NilInstrumentsDoesNotPanic(t *testing.T) {
	job := &queue.Job{ID: uuid.New(), DocumentID: uuid.New(), UserID: uuid.New()}
	consumer := &stubConsumer{jobs: []*queue.Job{job}}
	handler := &stubHandler{}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	w := worker.New(consumer, handler, worker.Config{PollInterval: 10 * time.Millisecond})
	_ = w.Run(ctx)

	if len(handler.handledJobs) != 1 {
		t.Fatalf("Handle calls: got %d, want 1", len(handler.handledJobs))
	}
}
