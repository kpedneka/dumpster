// Package awsbatch is the AWS SDK adapter for submitting and polling AWS
// Batch jobs. No AWS SDK types cross this package boundary — callers see
// only the domain-shaped types below, so entity/awsbatch (and anything
// else built on this later) is testable against the mock without needing
// real AWS credentials or network access, matching this repo's existing
// dependency-inversion pattern for queue/storage/LLM adapters.
package awsbatch

import "context"

// Status is a job's coarse lifecycle state, collapsing AWS Batch's finer
// SUBMITTED/PENDING/RUNNABLE/STARTING/RUNNING states into one "still
// going" bucket — callers polling for completion only ever care about the
// three states below, not Batch's internal scheduling detail.
type Status string

const (
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// SubmitJobParams describes one job submission. Environment values are
// passed to the container as environment variables, overriding whatever
// the job definition itself specifies.
type SubmitJobParams struct {
	JobName       string
	JobQueue      string
	JobDefinition string
	Environment   map[string]string
}

// JobState is a job's current status plus, once terminal, the detail
// needed to explain a failure (Reason) or fetch a success's output
// (Logs, via Client.JobLogs).
type JobState struct {
	Status Status
	Reason string // populated by AWS Batch when Status is StatusFailed
}

// Client is the boundary over AWS Batch (job submission/status) and
// CloudWatch Logs (job output) — the two AWS services a caller needs to
// submit a job and retrieve its result.
type Client interface {
	// SubmitJob submits a new job and returns its job ID.
	SubmitJob(ctx context.Context, params SubmitJobParams) (jobID string, err error)
	// JobState returns jobID's current status.
	JobState(ctx context.Context, jobID string) (JobState, error)
	// JobLogs returns the full captured stdout/stderr for jobID. Only
	// meaningful once the job has reached a terminal state; AWS Batch may
	// not have created a log stream yet for a job that never started.
	JobLogs(ctx context.Context, jobID string) (string, error)
}
