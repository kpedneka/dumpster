package awsbatch

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/batch"
	batchtypes "github.com/aws/aws-sdk-go-v2/service/batch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
)

// batchLogGroup is the fixed CloudWatch Logs group AWS Batch writes every
// job's container output to — not configurable per job definition unless a
// custom log configuration overrides it, which this pilot's job definition
// doesn't.
const batchLogGroup = "/aws/batch/job"

// realClient implements Client against the real AWS Batch and CloudWatch
// Logs APIs.
type realClient struct {
	batch *batch.Client
	logs  *cloudwatchlogs.Client
}

// NewClient builds a Client using the standard AWS credential chain (env
// vars for local dev, or the ECS task role in every real deployment —
// whichever the deployment environment provides).
func NewClient(ctx context.Context, region string) (Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("awsbatch: load AWS config: %w", err)
	}
	return &realClient{
		batch: batch.NewFromConfig(cfg),
		logs:  cloudwatchlogs.NewFromConfig(cfg),
	}, nil
}

func (c *realClient) SubmitJob(ctx context.Context, params SubmitJobParams) (string, error) {
	env := make([]batchtypes.KeyValuePair, 0, len(params.Environment))
	for k, v := range params.Environment {
		env = append(env, batchtypes.KeyValuePair{Name: aws.String(k), Value: aws.String(v)})
	}

	out, err := c.batch.SubmitJob(ctx, &batch.SubmitJobInput{
		JobName:            aws.String(params.JobName),
		JobQueue:           aws.String(params.JobQueue),
		JobDefinition:      aws.String(params.JobDefinition),
		ContainerOverrides: &batchtypes.ContainerOverrides{Environment: env},
	})
	if err != nil {
		return "", fmt.Errorf("awsbatch: submit job: %w", err)
	}
	return aws.ToString(out.JobId), nil
}

func (c *realClient) JobState(ctx context.Context, jobID string) (JobState, error) {
	out, err := c.batch.DescribeJobs(ctx, &batch.DescribeJobsInput{Jobs: []string{jobID}})
	if err != nil {
		return JobState{}, fmt.Errorf("awsbatch: describe job %q: %w", jobID, err)
	}
	if len(out.Jobs) == 0 {
		return JobState{}, fmt.Errorf("awsbatch: describe job %q: not found", jobID)
	}

	job := out.Jobs[0]
	switch job.Status {
	case batchtypes.JobStatusSucceeded:
		return JobState{Status: StatusSucceeded}, nil
	case batchtypes.JobStatusFailed:
		reason := aws.ToString(job.StatusReason)
		if job.Container != nil && aws.ToString(job.Container.Reason) != "" {
			reason = aws.ToString(job.Container.Reason)
		}
		return JobState{Status: StatusFailed, Reason: reason}, nil
	default:
		return JobState{Status: StatusRunning}, nil
	}
}

func (c *realClient) JobLogs(ctx context.Context, jobID string) (string, error) {
	out, err := c.batch.DescribeJobs(ctx, &batch.DescribeJobsInput{Jobs: []string{jobID}})
	if err != nil {
		return "", fmt.Errorf("awsbatch: describe job %q for logs: %w", jobID, err)
	}
	if len(out.Jobs) == 0 || out.Jobs[0].Container == nil {
		return "", fmt.Errorf("awsbatch: job %q has no container detail", jobID)
	}
	streamName := aws.ToString(out.Jobs[0].Container.LogStreamName)
	if streamName == "" {
		return "", fmt.Errorf("awsbatch: job %q has no log stream (never started running?)", jobID)
	}

	logsOut, err := c.logs.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(batchLogGroup),
		LogStreamName: aws.String(streamName),
		StartFromHead: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("awsbatch: get log events for stream %q: %w", streamName, err)
	}

	lines := make([]string, 0, len(logsOut.Events))
	for _, e := range logsOut.Events {
		lines = append(lines, aws.ToString(e.Message))
	}
	return strings.Join(lines, "\n"), nil
}

var _ Client = (*realClient)(nil)
