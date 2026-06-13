package auth

import "context"

type contextKey string

const userIDKey contextKey = "userID"

// WithUserID stores the authenticated user's ID in the context.
func WithUserID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// UserIDFromContext retrieves the authenticated user's ID.
// Returns 0 and false if not set.
func UserIDFromContext(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userIDKey).(int64)
	return id, ok
}
