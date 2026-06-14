package kb

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// KnowledgeBase is a named collection of documents belonging to one user.
type KnowledgeBase struct {
	ID        uuid.UUID `json:"id"`
	UserID    uuid.UUID `json:"user_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Repository is the persistence boundary for KnowledgeBase records.
// Every method is tenant-scoped: userID is always a filter, never optional.
type Repository interface {
	Create(ctx context.Context, userID uuid.UUID, name string) (*KnowledgeBase, error)
	Get(ctx context.Context, userID, id uuid.UUID) (*KnowledgeBase, error)
	List(ctx context.Context, userID uuid.UUID) ([]*KnowledgeBase, error)
	Rename(ctx context.Context, userID, id uuid.UUID, name string) (*KnowledgeBase, error)
	Delete(ctx context.Context, userID, id uuid.UUID) error
}
