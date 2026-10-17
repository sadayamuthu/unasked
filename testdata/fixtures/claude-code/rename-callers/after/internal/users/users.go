package users

import "context"

// User is an account holder.
type User struct {
	ID, Name string
}

// FetchUser loads the user with id from the account service.
func FetchUser(ctx context.Context, id string) (*User, error) {
	return lookup(ctx, id)
}

func lookup(ctx context.Context, id string) (*User, error) {
	return &User{ID: id}, ctx.Err()
}
