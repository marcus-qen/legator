package main

import "testing"

func TestVersionMetadataDefaults(t *testing.T) {
	if version != "dev" {
		t.Fatalf("expected default version %q, got %q", "dev", version)
	}
	if commit != "unknown" {
		t.Fatalf("expected default commit %q, got %q", "unknown", commit)
	}
	if date == "" {
		t.Fatal("expected default build date to be non-empty")
	}
}
