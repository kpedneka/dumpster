// Package cloudwatch implements queuemetrics.MetricPublisher against the
// real AWS CloudWatch PutMetricData API.
package cloudwatch

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/queuemetrics"
)

// Namespace groups every metric this package publishes -- referenced
// directly by terraform/ecs_worker_autoscaling.tf's CloudWatch alarms and
// by the IAM policy scoping the worker task role's PutMetricData grant to
// just this one namespace.
const Namespace = "Dumpster/Worker"

// TotalMetricName is the aggregate metric the worker's ECS step-scaling
// policy actually watches. One combined number across every job type, not
// a metric per type: the worker pool is generic and dequeues any pending
// job regardless of type, so total backlog -- not any one type's backlog
// -- is what should drive replica count.
const TotalMetricName = "PendingJobsTotal"

// PerTypeMetricName is the per-job-type breakdown, published for
// observability/dashboards only -- no scaling policy reads it.
const PerTypeMetricName = "PendingJobsByType"

// Client implements queuemetrics.MetricPublisher.
type Client struct {
	cw *cloudwatch.Client
}

// NewClient builds a Client using the standard AWS credential chain (an ECS
// task role in this project's deployments) -- same pattern as
// internal/awsbatch.NewClient.
func NewClient(ctx context.Context, region string) (*Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("queuemetrics/cloudwatch: load AWS config: %w", err)
	}
	return &Client{cw: cloudwatch.NewFromConfig(cfg)}, nil
}

// PutPendingJobs publishes byType's per-job-type breakdown plus their sum
// (TotalMetricName) in a single PutMetricData call. CloudWatch's own limit
// is 1000 data points per call; the real job-type count (five today, see
// queue.JobType's constants) is nowhere close, so no batching is needed --
// worth naming the ceiling so a future job type doesn't quietly approach it
// unnoticed.
func (c *Client) PutPendingJobs(ctx context.Context, byType map[queue.JobType]int) error {
	data := make([]cwtypes.MetricDatum, 0, len(byType)+1)
	total := 0
	for jobType, count := range byType {
		total += count
		data = append(data, cwtypes.MetricDatum{
			MetricName: aws.String(PerTypeMetricName),
			Value:      aws.Float64(float64(count)),
			Unit:       cwtypes.StandardUnitCount,
			Dimensions: []cwtypes.Dimension{
				{Name: aws.String("JobType"), Value: aws.String(string(jobType))},
			},
		})
	}
	data = append(data, cwtypes.MetricDatum{
		MetricName: aws.String(TotalMetricName),
		Value:      aws.Float64(float64(total)),
		Unit:       cwtypes.StandardUnitCount,
	})

	_, err := c.cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace:  aws.String(Namespace),
		MetricData: data,
	})
	if err != nil {
		return fmt.Errorf("queuemetrics/cloudwatch: put metric data: %w", err)
	}
	return nil
}

var _ queuemetrics.MetricPublisher = (*Client)(nil)
