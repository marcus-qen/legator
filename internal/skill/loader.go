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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Loader loads skills from various sources.
type Loader struct {
	client    client.Client
	namespace string
}

// NewLoader creates a new skill loader.
func NewLoader(c client.Client, namespace string) *Loader {
	return &Loader{client: c, namespace: namespace}
}

// Load loads a skill from the given source string.
// Source formats:
//   - "bundled" — load from bundled skills (embedded in controller)
//   - "configmap://name" or "configmap://name/key" — load from ConfigMap
//   - "git://github.com/org/repo#path@ref" — load from Git (stub for Phase 7)
func (l *Loader) Load(ctx context.Context, name, source string) (*Skill, error) {
	switch {
	case source == "bundled":
		return l.loadBundled(name)
	case strings.HasPrefix(source, "configmap://"):
		return l.loadFromConfigMap(ctx, name, source)
	case strings.HasPrefix(source, "git://"):
		return nil, fmt.Errorf("git source not yet implemented (Phase 7): %s", source)
	default:
		// Try as ConfigMap name for backwards compat
		return l.loadFromConfigMap(ctx, name, "configmap://"+source)
	}
}

// loadBundled loads a skill from the bundled skill registry.
// In Phase 1, bundled skills return a stub that references the skill name.
// Real bundled skills will be embedded in the controller binary.
func (l *Loader) loadBundled(name string) (*Skill, error) {
	return &Skill{
		Name:         name,
		Description:  fmt.Sprintf("Bundled skill: %s", name),
		Version:      "bundled",
		Instructions: fmt.Sprintf("(Bundled skill '%s' — instructions loaded at runtime from embedded filesystem)", name),
	}, nil
}

// loadFromConfigMap loads a skill from a Kubernetes ConfigMap.
// The ConfigMap should have:
//   - key "SKILL.md" — the skill markdown with YAML frontmatter
//   - key "actions.yaml" (optional) — the Action Sheet
func (l *Loader) loadFromConfigMap(ctx context.Context, name, source string) (*Skill, error) {
	// Parse "configmap://name" or "configmap://name/key"
	cmRef := strings.TrimPrefix(source, "configmap://")
	cmName := cmRef
	mdKey := "SKILL.md"
	if idx := strings.Index(cmRef, "/"); idx > 0 {
		cmName = cmRef[:idx]
		mdKey = cmRef[idx+1:]
	}

	cm := &corev1.ConfigMap{}
	if err := l.client.Get(ctx, types.NamespacedName{
		Name:      cmName,
		Namespace: l.namespace,
	}, cm); err != nil {
		return nil, fmt.Errorf("failed to load ConfigMap %q: %w", cmName, err)
	}

	mdContent, ok := cm.Data[mdKey]
	if !ok {
		return nil, fmt.Errorf("ConfigMap %q has no key %q", cmName, mdKey)
	}

	skill, err := Parse(mdContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse skill from ConfigMap %q: %w", cmName, err)
	}

	// Load actions.yaml if present
	if actionsYAML, ok := cm.Data["actions.yaml"]; ok {
		sheet, err := ParseActionSheet(actionsYAML)
		if err != nil {
			return nil, fmt.Errorf("failed to parse actions.yaml from ConfigMap %q: %w", cmName, err)
		}
		skill.Actions = sheet
	}

	return skill, nil
}

// Parse parses a SKILL.md string into a Skill struct.
// Expects YAML frontmatter between --- delimiters followed by markdown body.
func Parse(content string) (*Skill, error) {
	frontmatter, body, err := splitFrontmatter(content)
	if err != nil {
		return nil, err
	}

	skill := &Skill{
		Instructions: strings.TrimSpace(body),
	}

	if frontmatter != "" {
		var fm map[string]interface{}
		if err := yaml.Unmarshal([]byte(frontmatter), &fm); err != nil {
			return nil, fmt.Errorf("invalid YAML frontmatter: %w", err)
		}
		skill.RawFrontmatter = fm

		if v, ok := fm["name"].(string); ok {
			skill.Name = v
		}
		if v, ok := fm["description"].(string); ok {
			skill.Description = v
		}
		if v, ok := fm["version"].(string); ok {
			skill.Version = v
		}
		if v, ok := fm["license"].(string); ok {
			skill.License = v
		}
		if tags, ok := fm["tags"]; ok {
			if tagList, ok := tags.([]interface{}); ok {
				for _, t := range tagList {
					if s, ok := t.(string); ok {
						skill.Tags = append(skill.Tags, s)
					}
				}
			}
		}
	}

	return skill, nil
}

// ParseActionSheet parses an actions.yaml string into an ActionSheet.
func ParseActionSheet(content string) (*ActionSheet, error) {
	sheet := &ActionSheet{}
	if err := yaml.Unmarshal([]byte(content), sheet); err != nil {
		return nil, fmt.Errorf("invalid actions.yaml: %w", err)
	}
	return sheet, nil
}

// splitFrontmatter splits YAML frontmatter from markdown body.
func splitFrontmatter(content string) (frontmatter, body string, err error) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return "", content, nil
	}

	// Find the closing ---
	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", content, nil
	}

	frontmatter = strings.TrimSpace(rest[:idx])
	body = rest[idx+4:] // skip \n---
	return frontmatter, body, nil
}
