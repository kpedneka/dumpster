package worker

import (
	"errors"

	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
)

// retryable marks a handler error as worth retrying (pipeline.Transient)
// unless it means the document is gone, which fails the same way every
// time. The edge-extraction and canonicalization handlers' other failures
// all come from database or LLM calls, which are mostly network-level
// failures a retry can fix; pipeline.Transient itself leaves deterministic
// database errors (bad data, constraint violations) unmarked. The
// stateless-jobs Lambda's dispatcher reads the mark.
func retryable(err error) error {
	if errors.Is(err, document.ErrNotFound) {
		return err
	}
	return pipeline.Transient(err)
}
