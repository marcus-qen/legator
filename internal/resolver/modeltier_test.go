/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package resolver

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
)

func TestResolveTierFromConfig_Fast(t *testing.T) {
	config := &corev1alpha1.ModelTierConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: corev1alpha1.ModelTierConfigSpec{
			DefaultAuth: &corev1alpha1.ProviderAuthSpec{
				Type:      corev1alpha1.AuthAPIKey,
				SecretRef: "llm-api-key",
			},
			Tiers: []corev1alpha1.TierMapping{
				{
					Tier:                corev1alpha1.ModelTierFast,
					Provider:            "anthropic",
					Model:               "claude-haiku-3-5-20241022",
					MaxTokens:           4096,
					CostPerMillionInput: "0.80",
				},
				{
					Tier:     corev1alpha1.ModelTierStandard,
					Provider: "anthropic",
					Model:    "claude-sonnet-4-20250514",
				},
			},
		},
	}

	resolved, err := ResolveTierFromConfig(config, corev1alpha1.ModelTierFast)
	if err != nil {
		t.Fatalf("ResolveTierFromConfig() error = %v", err)
	}

	if resolved.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", resolved.Provider, "anthropic")
	}
	if resolved.Model != "claude-haiku-3-5-20241022" {
		t.Errorf("Model = %q, want %q", resolved.Model, "claude-haiku-3-5-20241022")
	}
	if resolved.FullModelString != "anthropic/claude-haiku-3-5-20241022" {
		t.Errorf("FullModelString = %q", resolved.FullModelString)
	}
	if resolved.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", resolved.MaxTokens)
	}
	// Should inherit default auth
	if resolved.Auth == nil || resolved.Auth.Type != corev1alpha1.AuthAPIKey {
		t.Error("expected inherited default auth")
	}
}

func TestResolveTierFromConfig_TierOverrideAuth(t *testing.T) {
	config := &corev1alpha1.ModelTierConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: corev1alpha1.ModelTierConfigSpec{
			DefaultAuth: &corev1alpha1.ProviderAuthSpec{
				Type:      corev1alpha1.AuthAPIKey,
				SecretRef: "default-key",
			},
			Tiers: []corev1alpha1.TierMapping{
				{
					Tier:     corev1alpha1.ModelTierFast,
					Provider: "ollama",
					Model:    "llama3",
					Endpoint: "http://localhost:11434",
					Auth: &corev1alpha1.ProviderAuthSpec{
						Type: corev1alpha1.AuthNone,
					},
				},
			},
		},
	}

	resolved, err := ResolveTierFromConfig(config, corev1alpha1.ModelTierFast)
	if err != nil {
		t.Fatalf("error = %v", err)
	}

	// Should use tier-specific auth, not default
	if resolved.Auth.Type != corev1alpha1.AuthNone {
		t.Errorf("Auth.Type = %q, want %q", resolved.Auth.Type, corev1alpha1.AuthNone)
	}
	if resolved.Endpoint != "http://localhost:11434" {
		t.Errorf("Endpoint = %q", resolved.Endpoint)
	}
}

func TestResolveTierFromConfig_NotFound(t *testing.T) {
	config := &corev1alpha1.ModelTierConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: corev1alpha1.ModelTierConfigSpec{
			Tiers: []corev1alpha1.TierMapping{
				{Tier: corev1alpha1.ModelTierFast, Provider: "anthropic", Model: "haiku"},
			},
		},
	}

	_, err := ResolveTierFromConfig(config, corev1alpha1.ModelTierReasoning)
	if err == nil {
		t.Error("expected error for missing tier")
	}
}
