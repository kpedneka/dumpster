// Package mock provides an in-memory awsbatch.Client for use in tests.
package mock

import (
	"context"
	"fmt"
	"sync"

	"github.com/kunalpednekar/dumpster/internal/awsbatch"
)

// Client is a test double: jobs "run" instantly, with the caller
// controlling each job's eventual state and logs via SetState/SetLogs.
// Since a job's ID isn't known until SubmitJob returns it — but a
// caller's whole Extract-equivalent call typically submits and then
// blocks polling in one synchronous call — tests configure a job's
// outcome ahead of time via NextJobID, which reports what the next
// SubmitJob call will return without actually submitting anything.
type Client struct {
	mu sync.Mutex

	// SubmitErr, when set, is returned by every SubmitJob call instead of
	// submitting.
	SubmitErr error

	nextJobID int
	states    map[string]awsbatch.JobState
	logs      map[string]string
	logsErr   map[string]error

	// SubmittedJobs records every SubmitJobParams passed to SubmitJob, in
	// order, so tests can assert on what was actually sent.
	SubmittedJobs []awsbatch.SubmitJobParams
}

func New() *Client {
	return &Client{
		states:  make(map[string]awsbatch.JobState),
		logs:    make(map[string]string),
		logsErr: make(map[string]error),
	}
}

// NextJobID returns the job ID the next SubmitJob call will return,
// without submitting anything — for pre-configuring that job's eventual
// SetState/SetLogs outcome before triggering the code under test.
func (c *Client) NextJobID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fmt.Sprintf("mock-job-%d", c.nextJobID+1)
}

// Jobs returns a snapshot of every SubmitJobParams passed to SubmitJob so
// far, in order. Unlike reading SubmittedJobs directly, this is safe to
// call from a goroutine other than the one driving the code under test —
// needed by any test that must observe a submission while the call that
// made it (e.g. Embed, which submits and then blocks awaiting the result)
// is still in flight.
func (c *Client) Jobs() []awsbatch.SubmitJobParams {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]awsbatch.SubmitJobParams, len(c.SubmittedJobs))
	copy(out, c.SubmittedJobs)
	return out
}

func (c *Client) SubmitJob(_ context.Context, params awsbatch.SubmitJobParams) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.SubmitErr != nil {
		return "", c.SubmitErr
	}
	c.nextJobID++
	jobID := fmt.Sprintf("mock-job-%d", c.nextJobID)
	c.SubmittedJobs = append(c.SubmittedJobs, params)
	return jobID, nil
}

// JobState reports StatusRunning for any job that hasn't had an explicit
// terminal state set via SetState yet — matching a real just-submitted
// Batch job, and letting tests move a job to a terminal state whenever
// they choose relative to when SubmitJob was actually called.
func (c *Client) JobState(_ context.Context, jobID string) (awsbatch.JobState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if state, ok := c.states[jobID]; ok {
		return state, nil
	}
	return awsbatch.JobState{Status: awsbatch.StatusRunning}, nil
}

func (c *Client) JobLogs(_ context.Context, jobID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err, ok := c.logsErr[jobID]; ok {
		return "", err
	}
	return c.logs[jobID], nil
}

// SetState sets the state JobState will report for jobID from now on —
// call after SubmitJob to move a job from its default StatusRunning to a
// terminal state, simulating the job finishing between polls.
func (c *Client) SetState(jobID string, state awsbatch.JobState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.states[jobID] = state
}

// SetLogs sets what JobLogs returns for jobID.
func (c *Client) SetLogs(jobID, logs string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs[jobID] = logs
}

// SetLogsErr sets the error JobLogs returns for jobID, instead of logs.
func (c *Client) SetLogsErr(jobID string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logsErr[jobID] = err
}

var _ awsbatch.Client = (*Client)(nil)
