// Package mock provides a test double for email.Sender.
package mock

import (
	"context"
	"sync"

	"github.com/kunalpednekar/dumpster/internal/email"
)

// Sender is an in-memory test double for email.Sender that records every
// message it was asked to send.
type Sender struct {
	mu   sync.Mutex
	sent []email.Message
	// Err, when set, is returned by every Send call.
	Err error
}

func New() *Sender {
	return &Sender{}
}

func (s *Sender) Send(_ context.Context, msg email.Message) error {
	if s.Err != nil {
		return s.Err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, msg)
	return nil
}

// Sent returns every message recorded so far.
func (s *Sender) Sent() []email.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]email.Message, len(s.sent))
	copy(out, s.sent)
	return out
}

// SentTo reports how many messages were sent to the given address.
func (s *Sender) SentTo(to string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.sent {
		if m.To == to {
			n++
		}
	}
	return n
}

var _ email.Sender = (*Sender)(nil)
