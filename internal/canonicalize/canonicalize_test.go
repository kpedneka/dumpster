package canonicalize

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/auth"
	canonicalmem "github.com/kunalpednekar/dumpster/internal/canonical/memory"
	"github.com/kunalpednekar/dumpster/internal/document"
	docmem "github.com/kunalpednekar/dumpster/internal/document/memory"
	"github.com/kunalpednekar/dumpster/internal/entity"
	entitymem "github.com/kunalpednekar/dumpster/internal/entity/memory"
	kbmem "github.com/kunalpednekar/dumpster/internal/kb/memory"
	"github.com/kunalpednekar/dumpster/internal/session"
	sessionmock "github.com/kunalpednekar/dumpster/internal/session/mock"
)

func seedSession(t *testing.T, sessions *sessionmock.Store) uuid.UUID {
	t.Helper()
	id := uuid.New()
	sessions.Seed(&session.Session{ID: id})
	return id
}

func TestRun_NoSessions_ReturnsZeroResult(t *testing.T) {
	b := New(sessionmock.New(), kbmem.New(), docmem.New(), entitymem.New(), canonicalmem.New())

	result, err := b.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Resolved != 0 || len(result.Errors) != 0 {
		t.Errorf("got %+v, want zero result", result)
	}
}

func TestRun_ResolvesUncanonicalizedMentions(t *testing.T) {
	sessions := sessionmock.New()
	kbs := kbmem.New()
	docs := docmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()

	userID := seedSession(t, sessions)
	ctx := auth.WithUserID(context.Background(), userID)

	k, err := kbs.Create(ctx, userID, "kb1")
	if err != nil {
		t.Fatal(err)
	}
	d, err := docs.Create(ctx, &document.Document{
		KBID: k.ID, UserID: userID, Filename: "f.txt",
		S3Key: "k", ContentType: "text/plain", Status: document.StatusIndexed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := entities.BulkCreate(ctx, []*entity.Entity{
		{KBID: k.ID, UserID: userID, DocumentID: d.ID, ChunkID: uuid.New(), Type: "person", Text: "Ada Lovelace"},
		{KBID: k.ID, UserID: userID, DocumentID: d.ID, ChunkID: uuid.New(), Type: "person", Text: "Ada Lovelace"},
	}); err != nil {
		t.Fatal(err)
	}

	b := New(sessions, kbs, docs, entities, canonicalRepo)
	result, err := b.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Resolved != 2 {
		t.Errorf("Resolved: got %d, want 2", result.Resolved)
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}

	mentions, _ := entities.ListByDocument(ctx, userID, d.ID)
	if mentions[0].CanonicalEntityID == nil || mentions[1].CanonicalEntityID == nil {
		t.Fatal("expected both mentions to be linked to a canonical entity")
	}
	if *mentions[0].CanonicalEntityID != *mentions[1].CanonicalEntityID {
		t.Error("both mentions share text/type and should resolve to the same canonical entity")
	}
	ce, err := canonicalRepo.Get(ctx, userID, *mentions[0].CanonicalEntityID)
	if err != nil {
		t.Fatal(err)
	}
	if ce.MentionCount != 2 {
		t.Errorf("MentionCount: got %d, want 2", ce.MentionCount)
	}
}

func TestRun_RerunSkipsAlreadyResolvedMentions(t *testing.T) {
	sessions := sessionmock.New()
	kbs := kbmem.New()
	docs := docmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()

	userID := seedSession(t, sessions)
	ctx := auth.WithUserID(context.Background(), userID)

	k, _ := kbs.Create(ctx, userID, "kb1")
	d, _ := docs.Create(ctx, &document.Document{
		KBID: k.ID, UserID: userID, Filename: "f.txt",
		S3Key: "k", ContentType: "text/plain", Status: document.StatusIndexed,
	})
	if err := entities.BulkCreate(ctx, []*entity.Entity{
		{KBID: k.ID, UserID: userID, DocumentID: d.ID, ChunkID: uuid.New(), Type: "person", Text: "Ada"},
	}); err != nil {
		t.Fatal(err)
	}

	b := New(sessions, kbs, docs, entities, canonicalRepo)
	if _, err := b.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := b.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Resolved != 0 {
		t.Errorf("second run: Resolved got %d, want 0 (nothing left to resolve)", second.Resolved)
	}

	mentions, _ := entities.ListByDocument(ctx, userID, d.ID)
	ce, err := canonicalRepo.Get(ctx, userID, *mentions[0].CanonicalEntityID)
	if err != nil {
		t.Fatal(err)
	}
	if ce.MentionCount != 1 {
		t.Errorf("re-run must not double-count: MentionCount got %d, want 1", ce.MentionCount)
	}
}

func TestRun_DocumentWithNoEntities_Skipped(t *testing.T) {
	sessions := sessionmock.New()
	kbs := kbmem.New()
	docs := docmem.New()
	entities := entitymem.New()
	canonicalRepo := canonicalmem.New()

	userID := seedSession(t, sessions)
	ctx := auth.WithUserID(context.Background(), userID)
	k, _ := kbs.Create(ctx, userID, "kb1")
	if _, err := docs.Create(ctx, &document.Document{
		KBID: k.ID, UserID: userID, Filename: "f.txt",
		S3Key: "k", ContentType: "text/plain", Status: document.StatusIndexed,
	}); err != nil {
		t.Fatal(err)
	}

	b := New(sessions, kbs, docs, entities, canonicalRepo)
	result, err := b.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Resolved != 0 || len(result.Errors) != 0 {
		t.Errorf("got %+v, want zero result", result)
	}
}
