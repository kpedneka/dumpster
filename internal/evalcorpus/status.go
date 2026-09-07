package evalcorpus

import (
	"fmt"
	"strings"

	"github.com/kunalpednekar/dumpster/internal/document"
)

// NotIndexedDocument names one document that hasn't reached "indexed"
// status yet, along with whatever status it's actually in.
type NotIndexedDocument struct {
	Filename string
	Status   document.Status
}

// IncompleteDocumentsError is returned by CheckAllIndexed when at least one
// document in a KB isn't fully processed. Scoring against a KB in this
// state produces a number that looks real but silently omits whatever a
// stuck, still-processing, or permanently-failed document would have
// contributed -- exactly the failure mode this type exists to catch loudly
// instead of letting it pass as a quiet gap in the score.
type IncompleteDocumentsError struct {
	NotIndexed []NotIndexedDocument
}

func (e *IncompleteDocumentsError) Error() string {
	parts := make([]string, len(e.NotIndexed))
	for i, d := range e.NotIndexed {
		parts[i] = fmt.Sprintf("%s (%s)", d.Filename, d.Status)
	}
	return fmt.Sprintf("evalcorpus: %d document(s) not yet indexed: %s", len(e.NotIndexed), strings.Join(parts, ", "))
}

// CheckAllIndexed returns an *IncompleteDocumentsError naming every
// document not in document.StatusIndexed, or nil if every document is
// ready. A document stuck in "pending"/"processing" just hasn't finished
// yet; one in "failed" (e.g. a dead-lettered AWS Batch job that exhausted
// its retries) never will without manual intervention -- either way, a
// caller should not treat a graphrecall/intrusion score as trustworthy
// until this returns nil.
func CheckAllIndexed(docs []*document.Document) error {
	var notIndexed []NotIndexedDocument
	for _, d := range docs {
		if d.Status != document.StatusIndexed {
			notIndexed = append(notIndexed, NotIndexedDocument{Filename: d.Filename, Status: d.Status})
		}
	}
	if len(notIndexed) == 0 {
		return nil
	}
	return &IncompleteDocumentsError{NotIndexed: notIndexed}
}
