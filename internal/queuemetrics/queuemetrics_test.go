package queuemetrics_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/queuemetrics"
)

type fakeCounter struct {
	counts map[queue.JobType]int
	err    error
}

func (f *fakeCounter) CountPending(context.Context) (map[queue.JobType]int, error) {
	return f.counts, f.err
}

type fakePublisher struct {
	got map[queue.JobType]int
	err error
}

func (f *fakePublisher) PutPendingJobs(_ context.Context, byType map[queue.JobType]int) error {
	f.got = byType
	return f.err
}

func TestPublish_ForwardsCountsToPublisher(t *testing.T) {
	counter := &fakeCounter{counts: map[queue.JobType]int{
		queue.JobTypeDocumentIndexing: 3,
		queue.JobTypeEntityExtraction: 1,
	}}
	publisher := &fakePublisher{}
	p := queuemetrics.New(counter, publisher)

	if err := p.Publish(context.Background()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(publisher.got) != 2 || publisher.got[queue.JobTypeDocumentIndexing] != 3 || publisher.got[queue.JobTypeEntityExtraction] != 1 {
		t.Errorf("publisher got %+v, want the counter's exact counts forwarded unchanged", publisher.got)
	}
}

func TestPublish_PropagatesCounterError(t *testing.T) {
	wantErr := errors.New("db down")
	p := queuemetrics.New(&fakeCounter{err: wantErr}, &fakePublisher{})

	err := p.Publish(context.Background())
	if err == nil || !errors.Is(err, wantErr) {
		t.Errorf("Publish error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestPublish_PropagatesPublisherError(t *testing.T) {
	wantErr := errors.New("cloudwatch unreachable")
	p := queuemetrics.New(&fakeCounter{counts: map[queue.JobType]int{}}, &fakePublisher{err: wantErr})

	err := p.Publish(context.Background())
	if err == nil || !errors.Is(err, wantErr) {
		t.Errorf("Publish error = %v, want it to wrap %v", err, wantErr)
	}
}

// TestPublish_EmptyQueueStillPublishes verifies that an empty backlog (the
// common case -- most polling intervals should find nothing pending) still
// calls the publisher, rather than skipping the call. A scaling policy
// needs a continuous, zero-inclusive metric stream to scale *in* reliably:
// if the metric simply stops being published whenever the queue is empty,
// CloudWatch treats the gap as "no data," and a target-tracking or
// step-scaling policy configured to require data can't act on it, silently
// freezing the current task count instead of scaling down.
func TestPublish_EmptyQueueStillPublishes(t *testing.T) {
	publisher := &fakePublisher{}
	p := queuemetrics.New(&fakeCounter{counts: map[queue.JobType]int{}}, publisher)

	if err := p.Publish(context.Background()); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if publisher.got == nil {
		t.Error("publisher should have been called even with an empty backlog")
	}
}
