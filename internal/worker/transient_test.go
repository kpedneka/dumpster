package worker_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	canonicalmem "github.com/kunalpednekar/dumpster/internal/canonical/memory"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/entity"
	entitymem "github.com/kunalpednekar/dumpster/internal/entity/memory"
	graphedgemem "github.com/kunalpednekar/dumpster/internal/graphedge/memory"
	"github.com/kunalpednekar/dumpster/internal/pipeline"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

// These tests pin which handler failures are worth retrying when the
// handlers run on Lambda: a repository failure is (network, database), a
// missing document isn't.

// unreachableEntities fails every read, like a dropped database connection.
type unreachableEntities struct{ entity.Repository }

func (unreachableEntities) ListByDocument(context.Context, uuid.UUID, uuid.UUID) ([]*entity.Entity, error) {
	return nil, errors.New("connection reset by peer")
}

func handlersFor(entities entity.Repository, docs document.Repository) map[string]worker.Handler {
	return map[string]worker.Handler{
		"edge":             worker.NewEdgeHandler(docs, entities, graphedgemem.New()),
		"canonicalization": worker.NewCanonicalizationHandler(docs, entities, canonicalmem.New()),
	}
}

func TestHandlers_MissingDocument_IsNotTransient(t *testing.T) {
	userID := uuid.New()
	ctx := auth.WithUserID(context.Background(), userID)
	for name, h := range handlersFor(entitymem.New(), docmem.New()) {
		err := h.Handle(ctx, &queue.Job{DocumentID: uuid.New(), UserID: userID})
		if err == nil || pipeline.IsTransient(err) {
			t.Errorf("%s: err = %v, want a non-transient error", name, err)
		}
	}
}

func TestHandlers_RepositoryFailure_IsTransient(t *testing.T) {
	userID := uuid.New()
	ctx := auth.WithUserID(context.Background(), userID)
	docs := docmem.New()
	doc, err := docs.Create(ctx, &document.Document{KBID: uuid.New(), UserID: userID, Filename: "f", S3Key: "k", Status: document.StatusIndexed})
	if err != nil {
		t.Fatalf("create document: %v", err)
	}
	for name, h := range handlersFor(unreachableEntities{entitymem.New()}, docs) {
		err := h.Handle(ctx, &queue.Job{DocumentID: doc.ID, UserID: userID})
		if !pipeline.IsTransient(err) {
			t.Errorf("%s: err = %v, want a transient error", name, err)
		}
	}
}
