/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/marcus-qen/legator/internal/skills"
)

func handleSkill(args []string) {
	if len(args) == 0 {
		fmt.Println(`legator skill — manage OCI skill artifacts

Usage:
  legator skill pack <directory>              Package a skill directory
  legator skill push <directory> <oci-ref>    Package and push to registry
  legator skill pull <oci-ref> [directory]    Pull from registry
  legator skill inspect <directory>           Show skill manifest`)
		os.Exit(1)
	}

	sub := args[0]
	rest := args[1:]

	switch sub {
	case "pack":
		handleSkillPack(rest)
	case "push":
		handleSkillPush(rest)
	case "pull":
		handleSkillPull(rest)
	case "inspect":
		handleSkillInspect(rest)
	default:
		fmt.Fprintf(os.Stderr, "Unknown skill subcommand: %s\n", sub)
		os.Exit(1)
	}
}

func handleSkillPack(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: legator skill pack <directory>")
		os.Exit(1)
	}

	dir := args[0]
	result, err := skills.Pack(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("📦 Packed skill: %s\n", result.Manifest.Name)
	fmt.Printf("   Description: %s\n", result.Manifest.Description)
	fmt.Printf("   Files: %d\n", len(result.Manifest.Files))
	for _, f := range result.Manifest.Files {
		fmt.Printf("     📄 %s\n", f)
	}
	fmt.Printf("   Config: %d bytes\n", len(result.Config))
	fmt.Printf("   Content: %d bytes\n", len(result.Content))
	fmt.Println()
	fmt.Println("✅ Skill packaged successfully. Use 'legator skill push' to upload to a registry.")
}

func handleSkillPush(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: legator skill push <directory> <oci-ref>")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Example: legator skill push ./my-skill oci://ghcr.io/my-org/my-skill:v1.0")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Options:")
		fmt.Fprintln(os.Stderr, "  --plain-http    Use HTTP instead of HTTPS (for dev registries)")
		os.Exit(1)
	}

	dir := args[0]
	ref := args[1]
	plainHTTP := false
	for _, a := range args[2:] {
		if a == "--plain-http" {
			plainHTTP = true
		}
	}

	// Parse reference
	ociRef, err := skills.ParseOCIRef(ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid OCI reference: %v\n", err)
		os.Exit(1)
	}

	// Push via ORAS
	client := skills.NewRegistryClient().WithPlainHTTP(plainHTTP)

	// Check for registry credentials in env
	if u := os.Getenv("LEGATOR_REGISTRY_USERNAME"); u != "" {
		client.WithAuth(u, os.Getenv("LEGATOR_REGISTRY_PASSWORD"))
	}

	fmt.Printf("📦 Packaging %s...\n", dir)
	result, err := client.Push(context.Background(), dir, ociRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Pushed to %s\n", result.Ref)
	fmt.Printf("   Digest: %s\n", result.Digest)
	fmt.Printf("   Config: %d bytes\n", result.ConfigSize)
	fmt.Printf("   Content: %d bytes\n", result.ContentSize)
	fmt.Printf("   Files: %d\n", len(result.Files))
	for _, f := range result.Files {
		fmt.Printf("     📄 %s\n", f)
	}
}

func handleSkillPull(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: legator skill pull <oci-ref> [directory]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Options:")
		fmt.Fprintln(os.Stderr, "  --plain-http    Use HTTP instead of HTTPS")
		os.Exit(1)
	}

	ref := args[0]
	plainHTTP := false
	destDir := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "--plain-http" {
			plainHTTP = true
		} else if destDir == "" {
			destDir = args[i]
		}
	}

	ociRef, err := skills.ParseOCIRef(ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid OCI reference: %v\n", err)
		os.Exit(1)
	}

	if destDir == "" {
		// Use last path segment as directory name
		parts := strings.Split(ociRef.Path, "/")
		destDir = parts[len(parts)-1]
	}

	client := skills.NewRegistryClient().WithPlainHTTP(plainHTTP)
	if u := os.Getenv("LEGATOR_REGISTRY_USERNAME"); u != "" {
		client.WithAuth(u, os.Getenv("LEGATOR_REGISTRY_PASSWORD"))
	}

	fmt.Printf("📥 Pulling %s → %s\n", ociRef.String(), destDir)
	result, err := client.PullToDir(context.Background(), ociRef, destDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Pulled %s\n", result.Ref)
	fmt.Printf("   Digest: %s\n", result.Digest)
	if len(result.Files) > 0 {
		fmt.Printf("   Files:\n")
		for _, f := range result.Files {
			fmt.Printf("     📄 %s\n", f)
		}
	}
}

func handleSkillInspect(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: legator skill inspect <directory>")
		os.Exit(1)
	}

	dir := args[0]
	result, err := skills.Pack(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("🔍 Skill: %s\n", result.Manifest.Name)
	fmt.Printf("   Description: %s\n", result.Manifest.Description)
	fmt.Printf("   Created: %s\n", result.Manifest.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("   Files:\n")
	for _, f := range result.Manifest.Files {
		fmt.Printf("     📄 %s\n", f)
	}
	fmt.Printf("\n   Config:\n%s\n", string(result.Config))
}
