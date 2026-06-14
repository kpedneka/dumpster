// Package memory provides an in-memory queue.Publisher for use in tests.
package memory

import (
	"context"
	"sync"

	"github.com/kunalpednekar/dumpster/internal/queue"
)

// Publisher records published events in memory.
type Publisher struct {
	mu     sync.Mutex
	events []queue.DocumentUploaded
}

// New returns an empty in-memory Publisher.
func New() *Publisher { return &Publisher{} }

// PublishDocumentUploaded appends evt to the recorded event list.
func (p *Publisher) PublishDocumentUploaded(_ context.Context, evt queue.DocumentUploaded) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, evt)
	return nil
}

// Events returns a snapshot of all published events.
func (p *Publisher) Events() []queue.DocumentUploaded {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]queue.DocumentUploaded, len(p.events))
	copy(cp, p.events)
	return cp
}

var _ queue.Publisher = (*Publisher)(nil)
