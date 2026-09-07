// Package evalcorpus ingests the validation corpus (see the repo's
// validation/ directory) into a benchmark knowledge base, using the exact
// same document-creation and job-publishing steps a real document upload
// goes through.
//
// It deliberately stops there: it never touches entity extraction,
// embedding, or AWS Batch. Those stages already run as a separately
// deployed worker process against real production credentials, and this
// package's whole job is to hand that worker the same kind of job a real
// user upload would produce, not to duplicate or bypass it. A caller runs
// this, then waits on the same job-status mechanism a real upload would,
// before scoring anything.
package evalcorpus

import (
	"bytes"
	"fmt"
	"path"
	"strings"

	"context"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/document"
	"github.com/kunalpednekar/dumpster/internal/objectstore"
	"github.com/kunalpednekar/dumpster/internal/queue"
)

// contentTypes mirrors internal/server/dochandler.go's accepted text types.
// The validation corpus is plain .txt today; .md is included since it costs
// nothing and is a natural extension for future corpus files.
var contentTypes = map[string]string{
	".txt": "text/plain",
	".md":  "text/markdown",
}

// Ingester uploads corpus files and registers them as documents, following
// the same S3-key convention and publish step
// internal/server/dochandler.go's upload handler uses, so a benchmark
// document is indistinguishable from a real user upload to every stage
// downstream of this package.
type Ingester struct {
	Objects   objectstore.ObjectStore
	Documents document.Repository
	Publisher queue.Publisher
}

// IngestFile uploads content under relPath (the corpus-relative path, e.g.
// "maritime/01_biography.txt") and creates its Document row with that path
// preserved verbatim as Filename, so a later scoring step can resolve
// ground_truth.json's document references back to real document IDs by
// exact filename match. Returns an error for any extension not in
// contentTypes.
func (ing *Ingester) IngestFile(ctx context.Context, userID, kbID uuid.UUID, relPath string, content []byte) (*document.Document, error) {
	ext := strings.ToLower(path.Ext(relPath))
	contentType, ok := contentTypes[ext]
	if !ok {
		return nil, fmt.Errorf("evalcorpus: unsupported extension %q for %q", ext, relPath)
	}

	s3Key := fmt.Sprintf("evalcorpus/%s/%s/%s/%s", userID, kbID, uuid.New(), path.Base(relPath))
	if err := ing.Objects.Put(ctx, s3Key, bytes.NewReader(content), int64(len(content)), contentType); err != nil {
		return nil, fmt.Errorf("evalcorpus: put %q: %w", relPath, err)
	}

	doc, err := ing.Documents.Create(ctx, &document.Document{
		KBID:        kbID,
		UserID:      userID,
		Filename:    relPath,
		S3Key:       s3Key,
		ContentType: contentType,
		SizeBytes:   int64(len(content)),
		Status:      document.StatusPending,
	})
	if err != nil {
		return nil, fmt.Errorf("evalcorpus: create document record for %q: %w", relPath, err)
	}

	if err := ing.Publisher.PublishDocumentUploaded(ctx, queue.DocumentUploaded{DocumentID: doc.ID, UserID: userID}); err != nil {
		_ = ing.Documents.UpdateStatus(ctx, userID, doc.ID, document.StatusFailed)
		return nil, fmt.Errorf("evalcorpus: publish document uploaded for %q: %w", relPath, err)
	}

	return doc, nil
}

// IngestAll ingests every file in files (keyed by corpus-relative path) and
// returns the resulting documents keyed the same way. It stops and returns
// the first error encountered, leaving any already-ingested files in
// place -- re-running IngestAll against the same KB after fixing the cause
// will simply add more documents, since nothing here is idempotent on
// relPath (a benchmark KB is expected to be created fresh per run).
func (ing *Ingester) IngestAll(ctx context.Context, userID, kbID uuid.UUID, files map[string][]byte) (map[string]*document.Document, error) {
	results := make(map[string]*document.Document, len(files))
	for relPath, content := range files {
		doc, err := ing.IngestFile(ctx, userID, kbID, relPath, content)
		if err != nil {
			return nil, err
		}
		results[relPath] = doc
	}
	return results, nil
}
