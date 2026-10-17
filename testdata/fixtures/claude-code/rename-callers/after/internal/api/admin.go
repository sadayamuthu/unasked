package api

import (
	"context"

	"example.com/shop/internal/users"
)

type banRequest struct{ TargetID string }

func ban(ctx context.Context, req banRequest) error {
	target, err := users.FetchUser(ctx, req.TargetID)
	if err != nil {
		return err
	}
	return markBanned(target)
}

func markBanned(*users.User) error { return nil }
