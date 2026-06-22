// Package resend is the sole adapter package permitted to import the Resend
// Go SDK. It implements email.Sender by delivering messages through the
// Resend API.
package resend

import (
	"context"
	"fmt"

	resendsdk "github.com/resend/resend-go/v2"

	"github.com/kunalpednekar/dumpster/internal/email"
)

// Sender implements email.Sender using the Resend API.
type Sender struct {
	client *resendsdk.Client
	from   string
}

// New returns a Sender that authenticates against Resend using apiKey (the
// RESEND_API_KEY value) and sends every message from the given address.
func New(apiKey, from string) *Sender {
	return &Sender{client: resendsdk.NewClient(apiKey), from: from}
}

// Send delivers msg via the Resend API.
func (s *Sender) Send(ctx context.Context, msg email.Message) error {
	_, err := s.client.Emails.SendWithContext(ctx, &resendsdk.SendEmailRequest{
		From:    s.from,
		To:      []string{msg.To},
		Subject: msg.Subject,
		Html:    msg.HTML,
	})
	if err != nil {
		return fmt.Errorf("resend: send to %s: %w", msg.To, err)
	}
	return nil
}

var _ email.Sender = (*Sender)(nil)
