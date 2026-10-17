package users

import "context"

// User is an account holder.
type User struct {
	ID, Name string
}

// GetUser loads the user with id from the account service.
func GetUser(ctx context.Context, id string) (*User, error) {
	return lookup(ctx, id)
}

func lookup(ctx context.Context, id string) (*User, error) {
	return &User{ID: id}, ctx.Err()
}
