package main

import (
	"context"
	"fmt"

	"example.com/shop/internal/users"
)

func whois(args []string) error {
	u, err := users.FetchUser(context.Background(), args[0])
	if err != nil {
		return err
	}
	fmt.Println(u.Name)
	return nil
}
