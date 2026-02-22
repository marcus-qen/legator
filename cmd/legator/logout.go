package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func handleLogout(args []string) {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "Usage: legator logout")
		os.Exit(1)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fatal(fmt.Errorf("failed to resolve home directory: %w", err))
	}

	path := filepath.Join(home, ".config", "legator", "token.json")
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("ℹ️  No cached login token found.")
			return
		}
		fatal(fmt.Errorf("failed to remove token cache %s: %w", path, err))
	}

	fmt.Printf("✅ Logged out. Removed token cache: %s\n", path)
}
