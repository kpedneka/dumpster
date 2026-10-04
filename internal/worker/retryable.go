package worker

import (
	"errors"

	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
)

// retryable marks a handler error as worth retrying (pipeline.Transient)
// unless it means the document is gone, which fails the same way every
// time. The edge-extraction and canonicalization handlers' other failures
// all come from database or LLM calls, which are the network-level
// failures a retry can fix. Only the stateless-jobs Lambda's dispatcher
// reads the mark; for the ECS worker these are ordinary errors.
func retryable(err error) error {
	if errors.Is(err, document.ErrNotFound) {
		return err
	}
	return pipeline.Transient(err)
}
