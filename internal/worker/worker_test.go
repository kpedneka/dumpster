package worker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

// stubConsumer is a test double for queue.Consumer.
type stubConsumer struct {
	jobs             []*queue.Job
	idx              int
	ackedIDs         []uuid.UUID
	nackedIDs        []uuid.UUID
	deadLetterOnNack bool
}

func (c *stubConsumer) Dequeue(_ context.Context) (*queue.Job, error) {
	if c.idx >= len(c.jobs) {
		return nil, queue.ErrNoJobs
	}
	j := c.jobs[c.idx]
	c.idx++
	return j, nil
}

func (c *stubConsumer) Ack(_ context.Context, id uuid.UUID) error {
	c.ackedIDs = append(c.ackedIDs, id)
	return nil
}

func (c *stubConsumer) Nack(_ context.Context, id uuid.UUID, _ error) (bool, error) {
	c.nackedIDs = append(c.nackedIDs, id)
	return c.deadLetterOnNack, nil
}

// stubHandler is a test double for worker.Handler.
type stubHandler struct {
	handledJobs []*queue.Job
	failedJobs  []*queue.Job
	handleErr   error
}

func (h *stubHandler) Handle(_ context.Context, job *queue.Job) error {
	h.handledJobs = append(h.handledJobs, job)
	return h.handleErr
}

func (h *stubHandler) OnFailed(_ context.Context, job *queue.Job) {
	h.failedJobs = append(h.failedJobs, job)
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
