// Package email defines the transactional-email sending boundary used by
// the demo account lifecycle (welcome and TTL-warning messages).
// Implementations live in dedicated adapter subpackages (e.g.
// internal/email/resend) so the rest of the app never imports a vendor
// email SDK directly.
package email

import (
	"context"
	"fmt"
	"time"
)

// Message is a single transactional email to send.
type Message struct {
	To      string
	Subject string
	HTML    string
}

// Sender delivers transactional email.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// WelcomeMessage is sent on signup (triggered by Clerk's user.created
// webhook), disclosing plainly that this is a demo and data is deleted
// after about 7 days.
func WelcomeMessage(to string) Message {
	return Message{
		To:      to,
		Subject: "Welcome — about your demo account",
		HTML: "<p>Thanks for signing up.</p>" +
			"<p>This is a demo deployment: to minimize retained data, your account " +
			"and everything in it is automatically deleted about 7 days after signup. " +
			"Please don't upload anything sensitive.</p>",
	}
}

// WarningMessage is sent at day 6 of the account TTL, about 24h before the
// account and its data are hard-deleted at deletesAt.
func WarningMessage(to string, deletesAt time.Time) Message {
	return Message{
		To:      to,
		Subject: "Your demo account will be deleted soon",
		HTML: fmt.Sprintf(
			"<p>This is a reminder that this is a demo deployment.</p>"+
				"<p>Your account and all of its data will be permanently deleted on "+
				"%s. Export anything you want to keep before then.</p>",
			deletesAt.Format("2006-01-02"),
		),
	}
}
