package server

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/kunalpednekar/dumpster/internal/auth"
)

// webhookHandler receives Clerk's user lifecycle webhooks (delivered via
// Svix). It verifies the request signature and parses the event, but —
// per this card's scope — does not yet act on the events; a future card
// consumes user.created/user.deleted for account lifecycle logic (e.g.
// TTL/cleanup). For now this endpoint exists so Clerk's webhook delivery
// can be configured and exercised end-to-end ahead of that logic landing.
type webhookHandler struct {
	secret string
}

func registerWebhookRoutes(mux *http.ServeMux, secret string) {
	h := &webhookHandler{secret: secret}
	mux.HandleFunc("POST /webhooks/clerk", h.handle)
}

// handle verifies the Svix signature on an incoming Clerk webhook request
// and acknowledges it. See auth.VerifyWebhook for the signature scheme.
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

	// Lifecycle events are intentionally not acted on yet (out of scope for
	// this card); log receipt so delivery can be verified end-to-end.
	slog.Info("clerk webhook received", "type", evt.Type)

	w.WriteHeader(http.StatusOK)
}
