package jobs

import (
	"context"
	"fmt"

	"example.com/shop/internal/users"
)

type subscription struct{ OwnerID string }

func digest(ctx context.Context, sub subscription) error {
	owner, err := users.GetUser(ctx, sub.OwnerID)
	if err != nil {
		return fmt.Errorf("digest: %w", err)
	}
	return send(owner)
}

func send(*users.User) error { return nil }
