package auth

import (
	"context"
	"time"
)

// User is the identity record stored in the database.
type User struct {
	ID           int64
	Email        string
	PasswordHash string
	CreatedAt    time.Time
}

// UserStore is the persistence boundary for User records.
type UserStore interface {
	Create(ctx context.Context, email, passwordHash string) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	GetByID(ctx context.Context, id int64) (*User, error)
}
