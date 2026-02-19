/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package skill

import (
	"testing"
)

func TestParseGitSource_Full(t *testing.T) {
	gs, err := ParseGitSource("git://github.com/marcus-qen/infraagent-skills#watchman-light@v1.0.0")
	if err != nil {
		t.Fatalf("ParseGitSource() error = %v", err)
	}

	if gs.URL != "https://github.com/marcus-qen/infraagent-skills" {
		t.Errorf("URL = %q, want %q", gs.URL, "https://github.com/marcus-qen/infraagent-skills")
	}
	if gs.Path != "watchman-light" {
		t.Errorf("Path = %q, want %q", gs.Path, "watchman-light")
	}
	if gs.Ref != "v1.0.0" {
		t.Errorf("Ref = %q, want %q", gs.Ref, "v1.0.0")
	}
}

func TestParseGitSource_NoRef(t *testing.T) {
	gs, err := ParseGitSource("git://github.com/org/repo#skills/monitoring")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if gs.URL != "https://github.com/org/repo" {
		t.Errorf("URL = %q", gs.URL)
	}
	if gs.Path != "skills/monitoring" {
		t.Errorf("Path = %q, want %q", gs.Path, "skills/monitoring")
	}
	if gs.Ref != "" {
		t.Errorf("Ref = %q, want empty", gs.Ref)
	}
}

func TestParseGitSource_NoPath(t *testing.T) {
	gs, err := ParseGitSource("git://github.com/org/repo@main")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if gs.URL != "https://github.com/org/repo" {
		t.Errorf("URL = %q", gs.URL)
	}
	if gs.Path != "" {
		t.Errorf("Path = %q, want empty", gs.Path)
	}
	if gs.Ref != "main" {
		t.Errorf("Ref = %q, want %q", gs.Ref, "main")
	}
}

func TestParseGitSource_Minimal(t *testing.T) {
	gs, err := ParseGitSource("git://github.com/org/repo")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if gs.URL != "https://github.com/org/repo" {
		t.Errorf("URL = %q", gs.URL)
	}
	if gs.Path != "" || gs.Ref != "" {
		t.Errorf("Path=%q Ref=%q, want both empty", gs.Path, gs.Ref)
	}
}

func TestParseGitSource_NotGit(t *testing.T) {
	_, err := ParseGitSource("configmap://my-skill")
	if err == nil {
		t.Error("expected error for non-git source")
	}
}

func TestParseGitSource_EmptyURL(t *testing.T) {
	_, err := ParseGitSource("git://")
	if err == nil {
		t.Error("expected error for empty git URL")
	}
}

func TestGitSource_CacheKey(t *testing.T) {
	gs := &GitSource{
		URL:  "https://github.com/org/repo",
		Path: "skills/monitoring",
		Ref:  "v1.0.0",
	}
	key := gs.CacheKey()
	expected := "https://github.com/org/repo#skills/monitoring@v1.0.0"
	if key != expected {
		t.Errorf("CacheKey() = %q, want %q", key, expected)
	}
}

func TestGitSource_CacheKeyMinimal(t *testing.T) {
	gs := &GitSource{URL: "https://github.com/org/repo"}
	key := gs.CacheKey()
	if key != "https://github.com/org/repo" {
		t.Errorf("CacheKey() = %q", key)
	}
}

func TestGitSource_String(t *testing.T) {
	gs := &GitSource{
		URL:  "https://github.com/org/repo",
		Path: "watchman",
		Ref:  "v1.0.0",
	}
	s := gs.String()
	expected := "git://github.com/org/repo#watchman@v1.0.0"
	if s != expected {
		t.Errorf("String() = %q, want %q", s, expected)
	}
}

func TestGitSource_StringMinimal(t *testing.T) {
	gs := &GitSource{URL: "https://github.com/org/repo"}
	s := gs.String()
	if s != "git://github.com/org/repo" {
		t.Errorf("String() = %q", s)
	}
}
