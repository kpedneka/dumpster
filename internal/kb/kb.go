package kb

import (
	"context"
	"time"
)

// KnowledgeBase is a named collection of documents belonging to one user.
type KnowledgeBase struct {
	ID        int64
	UserID    int64
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Repository is the persistence boundary for KnowledgeBase records.
// Every method is tenant-scoped: userID is always a filter, never optional.
type Repository interface {
	Create(ctx context.Context, userID int64, name string) (*KnowledgeBase, error)
	Get(ctx context.Context, userID, id int64) (*KnowledgeBase, error)
	List(ctx context.Context, userID int64) ([]*KnowledgeBase, error)
	Delete(ctx context.Context, userID, id int64) error
}
