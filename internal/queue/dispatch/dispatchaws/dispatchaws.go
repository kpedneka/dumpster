// Package dispatchaws builds the production queue.Publisher.
package dispatchaws

import (
	"context"

	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/queue/dispatch"
)

// New builds a publisher from cfg.
func New(ctx context.Context, cfg *config.Config, status dispatch.StatusWriter) (*dispatch.Publisher, error) {
	return nil, nil
}
