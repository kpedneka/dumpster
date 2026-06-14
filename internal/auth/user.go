package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// User is the identity record stored in the database.
type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

// UserStore is the persistence boundary for User records.
type UserStore interface {
	Create(ctx context.Context, email, passwordHash string) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*User, error)
}
