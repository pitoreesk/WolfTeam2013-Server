package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"wtserver-go/internal/config"
	"wtserver-go/internal/store"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "create" && os.Args[1] != "password" {
		fmt.Fprintln(os.Stderr, "usage: wtadmin <create|password> <username>")
		fmt.Fprintln(os.Stderr, "set the new password in WT_ACCOUNT_PASSWORD")
		os.Exit(2)
	}
	password := os.Getenv("WT_ACCOUNT_PASSWORD")
	if password == "" {
		fmt.Fprintln(os.Stderr, "WT_ACCOUNT_PASSWORD must not be empty")
		os.Exit(2)
	}
	databaseURL := os.Getenv("WT_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = config.Default().DatabaseURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	backend, err := store.OpenPostgres(ctx, databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer backend.Close()
	if os.Args[1] == "create" {
		err = backend.Register(ctx, os.Args[2], password)
	} else {
		err = backend.ChangePassword(ctx, os.Args[2], password)
	}
	if err != nil {
		if errors.Is(err, store.ErrUsernameTaken) || errors.Is(err, store.ErrUserNotFound) || errors.Is(err, store.ErrInvalidUsername) || errors.Is(err, store.ErrInvalidPassword) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if os.Args[1] == "create" {
		fmt.Printf("account %q created\n", os.Args[2])
	} else {
		fmt.Printf("password for %q changed\n", os.Args[2])
	}
}
