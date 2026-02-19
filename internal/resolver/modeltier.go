/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package resolver

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
)

// ResolvedModel contains the resolved model details for an agent run.
type ResolvedModel struct {
	// Tier is the abstract tier name.
	Tier corev1alpha1.ModelTier

	// Provider is the LLM provider (e.g. "anthropic", "openai").
	Provider string

	// Model is the specific model ID.
	Model string

	// Endpoint is the API base URL (empty for default provider endpoints).
	Endpoint string

	// Auth is the authentication config.
	Auth *corev1alpha1.ProviderAuthSpec

	// MaxTokens is the max output tokens.
	MaxTokens int32

	// CostPerMillionInput for cost estimation.
	CostPerMillionInput string

	// CostPerMillionOutput for cost estimation.
	CostPerMillionOutput string

	// FullModelString is "provider/model" for logging.
	FullModelString string
}

// ModelTierResolver resolves abstract model tiers to concrete provider/model pairs.
type ModelTierResolver struct {
	client client.Client
}

// NewModelTierResolver creates a new resolver.
func NewModelTierResolver(c client.Client) *ModelTierResolver {
	return &ModelTierResolver{client: c}
}

// Resolve finds the ModelTierConfig in the cluster and resolves the given tier.
func (r *ModelTierResolver) Resolve(ctx context.Context, tier corev1alpha1.ModelTier) (*ResolvedModel, error) {
	// List all ModelTierConfigs (cluster-scoped)
	configList := &corev1alpha1.ModelTierConfigList{}
	if err := r.client.List(ctx, configList); err != nil {
		return nil, fmt.Errorf("failed to list ModelTierConfigs: %w", err)
	}

	if len(configList.Items) == 0 {
		return nil, fmt.Errorf("no ModelTierConfig found in cluster")
	}

	// Use the first one (or the one named "default" if multiple exist)
	var config *corev1alpha1.ModelTierConfig
	for i := range configList.Items {
		if configList.Items[i].Name == "default" {
			config = &configList.Items[i]
			break
		}
	}
	if config == nil {
		config = &configList.Items[0]
	}

	return ResolveTierFromConfig(config, tier)
}

// ResolveTierFromConfig resolves a tier from a specific ModelTierConfig.
// Exported for use in tests and the assembler.
func ResolveTierFromConfig(config *corev1alpha1.ModelTierConfig, tier corev1alpha1.ModelTier) (*ResolvedModel, error) {
	for _, mapping := range config.Spec.Tiers {
		if mapping.Tier == tier {
			resolved := &ResolvedModel{
				Tier:                 tier,
				Provider:             mapping.Provider,
				Model:                mapping.Model,
				Endpoint:             mapping.Endpoint,
				MaxTokens:            mapping.MaxTokens,
				CostPerMillionInput:  mapping.CostPerMillionInput,
				CostPerMillionOutput: mapping.CostPerMillionOutput,
				FullModelString:      fmt.Sprintf("%s/%s", mapping.Provider, mapping.Model),
			}

			// Auth: use tier-specific auth if set, otherwise fall back to default
			if mapping.Auth != nil {
				resolved.Auth = mapping.Auth
			} else if config.Spec.DefaultAuth != nil {
				resolved.Auth = config.Spec.DefaultAuth
			}

			return resolved, nil
		}
	}

	return nil, fmt.Errorf("no mapping found for tier %q in ModelTierConfig %q", tier, config.Name)
}
