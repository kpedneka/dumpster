package inquiry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/kunalpednekar/dumpster/internal/inquiry"
	"github.com/kunalpednekar/dumpster/internal/inquiry/memory"
)

// Compile-time check that the test double satisfies the domain interface.
var _ inquiry.Repository = (*memory.Repository)(nil)

func TestGetOrCreate_IdempotentPerKBAndUser(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID, kbID := uuid.New(), uuid.New()

	first, err := repo.GetOrCreate(ctx, userID, kbID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.GetOrCreate(ctx, userID, kbID)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("GetOrCreate should return the same inquiry on repeat calls: got %v, then %v", first.ID, second.ID)
	}
}

func TestGetOrCreate_DistinctPerKB(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID := uuid.New()
	kbA, kbB := uuid.New(), uuid.New()

	a, _ := repo.GetOrCreate(ctx, userID, kbA)
	b, _ := repo.GetOrCreate(ctx, userID, kbB)
	if a.ID == b.ID {
		t.Fatal("inquiries for different KBs should not share an ID")
	}
}

func TestGet_NotFound(t *testing.T) {
	repo := memory.New()
	_, err := repo.Get(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, inquiry.ErrNotFound) {
		t.Errorf("got %v, want inquiry.ErrNotFound", err)
	}
}

func TestGet_TenantIsolation(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	owner, other := uuid.New(), uuid.New()
	kbID := uuid.New()

	created, _ := repo.GetOrCreate(ctx, owner, kbID)

	if _, err := repo.Get(ctx, other, kbID); !errors.Is(err, inquiry.ErrNotFound) {
		t.Errorf("other tenant Get: got %v, want inquiry.ErrNotFound", err)
	}
	if got, err := repo.Get(ctx, owner, kbID); err != nil || got.ID != created.ID {
		t.Errorf("owner should still resolve the inquiry: got %+v, err %v", got, err)
	}
}

func TestAppendMessage_AssignsSequentialOrdinal(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	userID, kbID := uuid.New(), uuid.New()
	inq, _ := repo.GetOrCreate(ctx, userID, kbID)

	first, err := repo.AppendMessage(ctx, userID, &inquiry.Message{
		InquiryID: inq.ID, KBID: kbID, Role: inquiry.RoleUser, Content: "what is FEMA?",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.AppendMessage(ctx, userID, &inquiry.Message{
		InquiryID: inq.ID, KBID: kbID, Role: inquiry.RoleAssistant, Content: "FEMA is...",
		Citations: []inquiry.Citation{{Number: 1, Text: "source span"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if first.Ordinal != 0 {
		t.Errorf("first message ordinal: got %d, want 0", first.Ordinal)
	}
	if second.Ordinal != 1 {
		t.Errorf("second message ordinal: got %d, want 1", second.Ordinal)
	}

	messages, err := repo.ListMessages(ctx, userID, inq.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("ListMessages: got %d, want 2", len(messages))
	}
	if messages[0].Role != inquiry.RoleUser || messages[1].Role != inquiry.RoleAssistant {
		t.Errorf("messages should preserve turn order: got roles %v, %v", messages[0].Role, messages[1].Role)
	}
	if len(messages[1].Citations) != 1 {
		t.Errorf("assistant message should carry its citations: got %d", len(messages[1].Citations))
	}
}

func TestAppendMessage_UnknownInquiry(t *testing.T) {
	repo := memory.New()
	_, err := repo.AppendMessage(context.Background(), uuid.New(), &inquiry.Message{
		InquiryID: uuid.New(), Role: inquiry.RoleUser, Content: "query",
	})
	if !errors.Is(err, inquiry.ErrNotFound) {
		t.Errorf("got %v, want inquiry.ErrNotFound", err)
	}
}

func TestAppendMessage_CrossTenant(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	owner, other := uuid.New(), uuid.New()
	kbID := uuid.New()
	inq, _ := repo.GetOrCreate(ctx, owner, kbID)

	_, err := repo.AppendMessage(ctx, other, &inquiry.Message{
		InquiryID: inq.ID, KBID: kbID, Role: inquiry.RoleUser, Content: "hijack",
	})
	if !errors.Is(err, inquiry.ErrNotFound) {
		t.Errorf("cross-tenant append: got %v, want inquiry.ErrNotFound", err)
	}
}

func TestListMessages_TenantIsolation(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	owner, other := uuid.New(), uuid.New()
	kbID := uuid.New()
	inq, _ := repo.GetOrCreate(ctx, owner, kbID)
	_, _ = repo.AppendMessage(ctx, owner, &inquiry.Message{InquiryID: inq.ID, KBID: kbID, Role: inquiry.RoleUser, Content: "q"})

	if _, err := repo.ListMessages(ctx, other, inq.ID); !errors.Is(err, inquiry.ErrNotFound) {
		t.Errorf("other tenant ListMessages: got %v, want inquiry.ErrNotFound", err)
	}
}
