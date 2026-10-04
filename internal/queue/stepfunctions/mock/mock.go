// Package mock provides an in-memory stepfunctions.Client for use in tests.
package mock

import (
	"context"
	"sync"
)

// Started records one StartExecution call.
type Started struct {
	StateMachineARN string
	Name            string
	Input           string
}

// Client is a test double: StartExecution records the call instead of
// talking to real AWS, so tests can assert on exactly which state machine
// would have been started, under which name, with which input.
type Client struct {
	mu sync.Mutex

	// StartErr, when set, is returned by every StartExecution call instead
	// of recording it.
	StartErr error

	started []Started
}

// New returns an empty Client.
func New() *Client {
	return &Client{}
}

// StartExecution records the call, or returns StartErr if set.
func (c *Client) StartExecution(_ context.Context, stateMachineARN, name, input string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.StartErr != nil {
		return c.StartErr
	}
	c.started = append(c.started, Started{StateMachineARN: stateMachineARN, Name: name, Input: input})
	return nil
}

// Started returns a snapshot of every StartExecution call recorded so far,
// in order.
func (c *Client) Started() []Started {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Started, len(c.started))
	copy(out, c.started)
	return out
}
