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

func TestParse_FullFrontmatter(t *testing.T) {
	content := `---
name: endpoint-monitoring
description: Fast endpoint health probe
version: "1.0.0"
license: Apache-2.0
tags: [monitoring, endpoints, health-check]
---

# Endpoint Monitoring

Check all endpoints are responding.
`

	s, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if s.Name != "endpoint-monitoring" {
		t.Errorf("Name = %q, want %q", s.Name, "endpoint-monitoring")
	}
	if s.Description != "Fast endpoint health probe" {
		t.Errorf("Description = %q, want %q", s.Description, "Fast endpoint health probe")
	}
	if s.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", s.Version, "1.0.0")
	}
	if s.License != "Apache-2.0" {
		t.Errorf("License = %q, want %q", s.License, "Apache-2.0")
	}
	if len(s.Tags) != 3 {
		t.Errorf("Tags count = %d, want 3", len(s.Tags))
	}
	if !containsString(s.Instructions, "# Endpoint Monitoring") {
		t.Errorf("Instructions should contain heading, got: %s", s.Instructions[:50])
	}
}

func TestParse_NoFrontmatter(t *testing.T) {
	content := "# Just Markdown\n\nNo frontmatter here."
	s, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if s.Name != "" {
		t.Errorf("Name should be empty, got %q", s.Name)
	}
	if !containsString(s.Instructions, "Just Markdown") {
		t.Errorf("Instructions mismatch")
	}
}

func TestParse_EmptyContent(t *testing.T) {
	s, err := Parse("")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if s.Instructions != "" {
		t.Errorf("Instructions should be empty, got %q", s.Instructions)
	}
}

func TestParseActionSheet(t *testing.T) {
	content := `actions:
  - id: check-endpoint
    description: "HTTP health check"
    tool: http.get
    targetPattern: "{environment.endpoints.*.url}"
    tier: read
    sideEffects: none
  - id: restart-deployment
    description: "Rolling restart"
    tool: kubectl.rollout
    targetPattern: "restart deployment/*"
    tier: service-mutation
    cooldown: "300s"
    dataImpact: none
    preConditions:
      - check: "deployment has >0 ready replicas"
        failAction: abort
`

	sheet, err := ParseActionSheet(content)
	if err != nil {
		t.Fatalf("ParseActionSheet() error = %v", err)
	}

	if len(sheet.Actions) != 2 {
		t.Fatalf("Actions count = %d, want 2", len(sheet.Actions))
	}

	a := sheet.Actions[0]
	if a.ID != "check-endpoint" {
		t.Errorf("Action[0].ID = %q, want %q", a.ID, "check-endpoint")
	}
	if a.Tier != "read" {
		t.Errorf("Action[0].Tier = %q, want %q", a.Tier, "read")
	}

	b := sheet.Actions[1]
	if b.Cooldown != "300s" {
		t.Errorf("Action[1].Cooldown = %q, want %q", b.Cooldown, "300s")
	}
	if len(b.PreConditions) != 1 {
		t.Errorf("Action[1].PreConditions count = %d, want 1", len(b.PreConditions))
	}
}

func TestParseActionSheet_Empty(t *testing.T) {
	sheet, err := ParseActionSheet("actions: []")
	if err != nil {
		t.Fatalf("ParseActionSheet() error = %v", err)
	}
	if len(sheet.Actions) != 0 {
		t.Errorf("Actions count = %d, want 0", len(sheet.Actions))
	}
}

func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantFM   string
		wantBody string
	}{
		{
			name:     "with frontmatter",
			input:    "---\nname: test\n---\n# Body",
			wantFM:   "name: test",
			wantBody: "\n# Body",
		},
		{
			name:     "no frontmatter",
			input:    "# Just body",
			wantFM:   "",
			wantBody: "# Just body",
		},
		{
			name:     "empty",
			input:    "",
			wantFM:   "",
			wantBody: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, body, err := splitFrontmatter(tt.input)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if fm != tt.wantFM {
				t.Errorf("frontmatter = %q, want %q", fm, tt.wantFM)
			}
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && contains(s, sub))
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
