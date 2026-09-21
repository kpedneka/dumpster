// Package mock provides an in-memory sqs.Client for use in tests.
package mock

import (
	"context"
	"sync"
)

// Sent records one SendMessage call.
type Sent struct {
	QueueURL string
	Body     string
}

// Client is a test double: SendMessage records the call instead of talking
// to real AWS, so tests can assert on exactly what would have been
// published and to which queue.
type Client struct {
	mu sync.Mutex

	// SendErr, when set, is returned by every SendMessage call instead of
	// recording it.
	SendErr error

	sent []Sent
}

// New returns an empty Client.
func New() *Client {
	return &Client{}
}

// SendMessage records body as sent to queueURL, or returns SendErr if set.
func (c *Client) SendMessage(_ context.Context, queueURL, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.SendErr != nil {
		return c.SendErr
	}
	c.sent = append(c.sent, Sent{QueueURL: queueURL, Body: body})
	return nil
}

// Sent returns a snapshot of every SendMessage call recorded so far, in
// order.
func (c *Client) Sent() []Sent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Sent, len(c.sent))
	copy(out, c.sent)
	return out
}
