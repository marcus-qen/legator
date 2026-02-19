/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package skill

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitSource represents a parsed git:// source string.
//
// Format: git://host/org/repo#path@ref
//
// Examples:
//
//	git://github.com/marcus-qen/infraagent-skills#watchman-light@v1.0.0
//	git://github.com/org/repo#skills/monitoring@main
//	git://github.com/org/repo#skills/monitoring  (default ref: HEAD)
type GitSource struct {
	// URL is the full clone URL (https).
	URL string

	// Path is the subdirectory within the repo containing the skill.
	Path string

	// Ref is the git ref (tag, branch, or commit SHA). Empty means HEAD.
	Ref string
}

// ParseGitSource parses a git:// source string.
func ParseGitSource(source string) (*GitSource, error) {
	s := strings.TrimPrefix(source, "git://")
	if s == source {
		return nil, fmt.Errorf("not a git source: %q", source)
	}

	gs := &GitSource{}

	// Split ref (@ref)
	if idx := strings.LastIndex(s, "@"); idx > 0 {
		gs.Ref = s[idx+1:]
		s = s[:idx]
	}

	// Split path (#path)
	if idx := strings.Index(s, "#"); idx > 0 {
		gs.Path = s[idx+1:]
		s = s[:idx]
	}

	if s == "" {
		return nil, fmt.Errorf("empty git URL in source: %q", source)
	}

	// Convert host/org/repo to https URL
	gs.URL = "https://" + s

	return gs, nil
}

// loadFromGit clones a Git repo (sparse if possible) and loads the skill.
func (l *Loader) loadFromGit(ctx context.Context, name, source string) (*Skill, error) {
	gs, err := ParseGitSource(source)
	if err != nil {
		return nil, err
	}

	// Check cache first
	if l.cache != nil {
		cacheKey := gs.CacheKey()
		if cached, ok := l.cache.Get(cacheKey); ok {
			return cached, nil
		}
	}

	// Create temp dir for clone
	tmpDir, err := os.MkdirTemp("", "infraagent-skill-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Clone with depth 1 (shallow)
	if err := gitClone(ctx, gs, tmpDir); err != nil {
		return nil, fmt.Errorf("git clone %s: %w", gs.URL, err)
	}

	// Read SKILL.md
	skillDir := tmpDir
	if gs.Path != "" {
		skillDir = filepath.Join(tmpDir, gs.Path)
	}

	mdPath := filepath.Join(skillDir, "SKILL.md")
	mdContent, err := os.ReadFile(mdPath)
	if err != nil {
		return nil, fmt.Errorf("read SKILL.md from %s: %w", gs.URL, err)
	}

	skill, err := Parse(string(mdContent))
	if err != nil {
		return nil, fmt.Errorf("parse skill from %s: %w", gs.URL, err)
	}

	// Override name if not set in frontmatter
	if skill.Name == "" {
		skill.Name = name
	}

	// Load actions.yaml if present
	actionsPath := filepath.Join(skillDir, "actions.yaml")
	if actionsContent, err := os.ReadFile(actionsPath); err == nil {
		sheet, err := ParseActionSheet(string(actionsContent))
		if err != nil {
			return nil, fmt.Errorf("parse actions.yaml from %s: %w", gs.URL, err)
		}
		skill.Actions = sheet
	}

	// Store source metadata
	skill.Source = &SourceInfo{
		Type: "git",
		URL:  gs.URL,
		Ref:  gs.Ref,
		Path: gs.Path,
	}

	// Cache the result
	if l.cache != nil {
		l.cache.Put(gs.CacheKey(), skill)
	}

	return skill, nil
}

// gitClone performs a shallow clone of the repo at the given ref.
func gitClone(ctx context.Context, gs *GitSource, destDir string) error {
	args := []string{"clone", "--depth", "1"}

	if gs.Ref != "" {
		args = append(args, "--branch", gs.Ref)
	}

	args = append(args, "--single-branch", gs.URL, destDir)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", // Never prompt for credentials
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// If --branch failed (could be a commit SHA), try without it
		if gs.Ref != "" && strings.Contains(string(output), "not found") {
			return gitCloneWithCheckout(ctx, gs, destDir)
		}
		return fmt.Errorf("%s: %s", err, string(output))
	}

	return nil
}

// gitCloneWithCheckout clones then checks out a specific commit.
func gitCloneWithCheckout(ctx context.Context, gs *GitSource, destDir string) error {
	// Clone without ref
	cmd := exec.CommandContext(ctx, "git", "clone", "--single-branch", gs.URL, destDir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("clone: %s: %s", err, string(output))
	}

	// Checkout the specific ref
	cmd = exec.CommandContext(ctx, "git", "-C", destDir, "checkout", gs.Ref)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("checkout %s: %s: %s", gs.Ref, err, string(output))
	}

	return nil
}

// CacheKey returns a stable cache key for this git source.
func (gs *GitSource) CacheKey() string {
	key := gs.URL
	if gs.Path != "" {
		key += "#" + gs.Path
	}
	if gs.Ref != "" {
		key += "@" + gs.Ref
	}
	return key
}

// String returns the original source string.
func (gs *GitSource) String() string {
	s := "git://" + strings.TrimPrefix(gs.URL, "https://")
	if gs.Path != "" {
		s += "#" + gs.Path
	}
	if gs.Ref != "" {
		s += "@" + gs.Ref
	}
	return s
}
