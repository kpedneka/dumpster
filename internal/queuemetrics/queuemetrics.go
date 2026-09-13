// Package queuemetrics periodically forwards job-queue backlog depth to a
// metrics backend, for the worker's queue-depth-based ECS auto-scaling
// policy (see the "Design a queue-depth-based autoscaling signal for the
// worker" dev board card). The real backend is CloudWatch
// (internal/queuemetrics/cloudwatch); Publisher itself depends only on the
// MetricPublisher interface, so the logic here is testable without AWS.
package queuemetrics

import (
	"context"
	"fmt"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// MetricPublisher publishes a snapshot of pending-job counts, grouped by
// job type, to a metrics backend.
type MetricPublisher interface {
	PutPendingJobs(ctx context.Context, byType map[queue.JobType]int) error
}

// Publisher queries a queue.PendingCounter and forwards the result to a
// MetricPublisher. Meant to be driven by a caller-owned ticker (see
// cmd/worker's runQueueMetricsLoop) -- Publisher itself has no scheduling
// logic of its own.
type Publisher struct {
	counter   queue.PendingCounter
	publisher MetricPublisher
}

// New returns a Publisher that reads backlog depth from counter and
// forwards it to publisher.
func New(counter queue.PendingCounter, publisher MetricPublisher) *Publisher {
	return &Publisher{counter: counter, publisher: publisher}
}

// Publish queries the current pending-job counts and forwards them,
// unconditionally -- including when the queue is empty. A scaling policy
// needs a continuous, zero-inclusive metric stream to scale in reliably:
// skipping the call whenever the backlog is empty would leave CloudWatch
// with no data point for that period, and a policy configured to require
// data can't act on a gap the way it can act on an explicit zero.
func (p *Publisher) Publish(ctx context.Context) error {
	counts, err := p.counter.CountPending(ctx)
	if err != nil {
		return fmt.Errorf("queuemetrics: count pending: %w", err)
	}
	if err := p.publisher.PutPendingJobs(ctx, counts); err != nil {
		return fmt.Errorf("queuemetrics: put pending jobs: %w", err)
	}
	return nil
}
