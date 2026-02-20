/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package skills

import (
	"testing"
)

func TestRegistryClient_NewAndConfigure(t *testing.T) {
	rc := NewRegistryClient()
	if rc == nil {
		t.Fatal("expected non-nil client")
	}

	rc.WithAuth("user", "pass")
	if rc.Username != "user" {
		t.Errorf("username = %q, want user", rc.Username)
	}
	if rc.Password != "pass" {
		t.Errorf("password = %q, want pass", rc.Password)
	}

	rc.WithPlainHTTP(true)
	if !rc.PlainHTTP {
		t.Error("expected PlainHTTP = true")
	}
}

func TestRegistryClient_PushPackError(t *testing.T) {
	rc := NewRegistryClient()
	ref := &OCIRef{Registry: "localhost:5000", Path: "test/skill", Tag: "v1"}

	// Push a nonexistent directory should fail at pack stage
	_, err := rc.Push(t.Context(), "/nonexistent", ref)
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}

func TestRegistryClient_PullBadRegistry(t *testing.T) {
	rc := NewRegistryClient().WithPlainHTTP(true)
	ref := &OCIRef{Registry: "localhost:1", Path: "test/skill", Tag: "v1"}

	// Pull from unreachable registry should fail
	_, _, err := rc.Pull(t.Context(), ref)
	if err == nil {
		t.Error("expected error for unreachable registry")
	}
}

func TestPushResult_Fields(t *testing.T) {
	r := PushResult{
		Ref:         "oci://ghcr.io/org/skill:v1",
		Digest:      "sha256:abc",
		ConfigSize:  100,
		ContentSize: 5000,
		Files:       []string{"SKILL.md", "actions.yaml"},
	}

	if r.Ref != "oci://ghcr.io/org/skill:v1" {
		t.Error("ref mismatch")
	}
	if len(r.Files) != 2 {
		t.Error("files count mismatch")
	}
}

func TestPullResult_Fields(t *testing.T) {
	r := PullResult{
		Ref:    "oci://ghcr.io/org/skill:v1",
		Digest: "sha256:def",
		Size:   8000,
		Name:   "my-skill",
		Files:  []string{"SKILL.md"},
	}

	if r.Name != "my-skill" {
		t.Error("name mismatch")
	}
}
