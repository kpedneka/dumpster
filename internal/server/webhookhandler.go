package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/email"
)

// webhookHandler receives Clerk's user lifecycle webhooks (delivered via
// Svix). It verifies the request signature and parses the event, then acts
// on user.created by sending the welcome/demo-disclosure email. Other event
// types (including user.deleted) are intentionally left log-only: the
// account lifecycle sweep (internal/account.Sweep) is what drives deletion,
// and re-triggering cleanup from a user.deleted webhook would create a
// circular trigger (that event fires as a side effect of the sweep's own
// delete call) for no benefit, since no deletion-confirmation email is
// wanted.
type webhookHandler struct {
	secret string
	emails email.Sender
}

func registerWebhookRoutes(mux *http.ServeMux, secret string, emails email.Sender) {
	h := &webhookHandler{secret: secret, emails: emails}
	mux.HandleFunc("POST /webhooks/clerk", h.handle)
}

// handle verifies the Svix signature on an incoming Clerk webhook request,
// dispatches by event type, and acknowledges it. See auth.VerifyWebhook for
// the signature scheme.
func (h *webhookHandler) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	id := r.Header.Get("svix-id")
	timestamp := r.Header.Get("svix-timestamp")
	signature := r.Header.Get("svix-signature")
	if id == "" || timestamp == "" || signature == "" {
		writeError(w, http.StatusBadRequest, "missing svix signature headers")
		return
	}

	evt, err := auth.VerifyWebhook(h.secret, id, timestamp, body, signature)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid webhook signature")
		return
	}

	switch evt.Type {
	case "user.created":
		h.sendWelcomeEmail(r.Context(), evt)
	default:
		slog.Info("clerk webhook received", "type", evt.Type)
	}

	w.WriteHeader(http.StatusOK)
}

// sendWelcomeEmail unmarshals evt's user data and sends the welcome/demo-
// disclosure email. A send failure is logged, not surfaced as a webhook
// failure: Clerk retries non-2xx responses, and a delivery failure here
// shouldn't cause it to retry an already-processed signup.
func (h *webhookHandler) sendWelcomeEmail(ctx context.Context, evt *auth.WebhookEvent) {
	var data auth.WebhookUserData
	if err := json.Unmarshal(evt.Data, &data); err != nil {
		slog.Error("clerk webhook: failed to parse user.created data", "err", err)
		return
	}
	to := data.PrimaryEmail()
	if to == "" {
		slog.Error("clerk webhook: user.created has no email address", "clerk_user_id", data.ID)
		return
	}
	if err := h.emails.Send(ctx, email.WelcomeMessage(to)); err != nil {
		slog.Error("clerk webhook: failed to send welcome email", "clerk_user_id", data.ID, "err", err)
	}
}
