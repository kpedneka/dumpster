package email_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kunalpednekar/dumpster/internal/email"
)

func TestWelcomeMessage(t *testing.T) {
	msg := email.WelcomeMessage("alice@example.com")

	if msg.To != "alice@example.com" {
		t.Errorf("To: got %q, want alice@example.com", msg.To)
	}
	if msg.Subject == "" {
		t.Error("Subject should not be empty")
	}
	if msg.HTML == "" {
		t.Error("HTML should not be empty")
	}
	if !strings.Contains(strings.ToLower(msg.HTML), "demo") {
		t.Error("welcome email should disclose that this is a demo")
	}
	if !strings.Contains(msg.HTML, "7") {
		t.Error("welcome email should mention the ~7 day retention window")
	}
}

func TestWarningMessage(t *testing.T) {
	deletesAt := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	msg := email.WarningMessage("alice@example.com", deletesAt)

	if msg.To != "alice@example.com" {
		t.Errorf("To: got %q, want alice@example.com", msg.To)
	}
	if msg.Subject == "" {
		t.Error("Subject should not be empty")
	}
	if !strings.Contains(msg.HTML, "2026-06-27") {
		t.Errorf("warning email should mention the deletion date, got: %s", msg.HTML)
	}
}
