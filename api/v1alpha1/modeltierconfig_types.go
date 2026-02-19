/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AuthType defines how the controller authenticates with an LLM provider.
// +kubebuilder:validation:Enum=apiKey;oauth;serviceAccount;none;custom
type AuthType string

const (
	AuthAPIKey         AuthType = "apiKey"
	AuthOAuth          AuthType = "oauth"
	AuthServiceAccount AuthType = "serviceAccount"
	AuthNone           AuthType = "none"
	AuthCustom         AuthType = "custom"
)

// ProviderAuthSpec configures authentication for an LLM provider.
type ProviderAuthSpec struct {
	// type is the authentication method.
	// +required
	Type AuthType `json:"type"`

	// secretRef references a Secret containing auth credentials.
	// For apiKey: key "api-key". For oauth: keys "client-id", "client-secret", "token-url".
	// For custom: keys used as HTTP headers.
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// secretKey is the specific key within the Secret (for apiKey auth).
	// +optional
	// +kubebuilder:default="api-key"
	SecretKey string `json:"secretKey,omitempty"`
}

// TierMapping maps a model tier to a specific provider and model.
type TierMapping struct {
	// tier is the abstract tier name (fast/standard/reasoning).
	// +required
	Tier ModelTier `json:"tier"`

	// provider is the LLM provider name (e.g. "anthropic", "openai", "ollama").
	// +required
	Provider string `json:"provider"`

	// model is the specific model ID (e.g. "claude-sonnet-4-20250514", "gpt-4o").
	// +required
	Model string `json:"model"`

	// endpoint is the API base URL. Required for non-standard providers (Ollama, vLLM, etc.).
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// auth configures authentication for this specific tier mapping.
	// If unset, inherits from the top-level defaultAuth.
	// +optional
	Auth *ProviderAuthSpec `json:"auth,omitempty"`

	// maxTokens is the max output tokens for this tier.
	// +optional
	MaxTokens int32 `json:"maxTokens,omitempty"`

	// costPerMillionInput is the estimated cost per million input tokens (USD).
	// Used for cost reporting in AgentRuns.
	// +optional
	CostPerMillionInput string `json:"costPerMillionInput,omitempty"`

	// costPerMillionOutput is the estimated cost per million output tokens (USD).
	// +optional
	CostPerMillionOutput string `json:"costPerMillionOutput,omitempty"`
}

// ModelTierConfigSpec defines the tier-to-model mappings.
type ModelTierConfigSpec struct {
	// defaultAuth is the default authentication config for all tiers
	// unless overridden per tier.
	// +optional
	DefaultAuth *ProviderAuthSpec `json:"defaultAuth,omitempty"`

	// tiers maps abstract tier names to specific provider/model pairs.
	// +required
	// +kubebuilder:validation:MinItems=1
	Tiers []TierMapping `json:"tiers"`
}

// ModelTierConfigStatus defines the observed state.
type ModelTierConfigStatus struct {
	// ready indicates all configured tiers are reachable.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// tierStatus reports per-tier health.
	// +optional
	TierStatus map[string]string `json:"tierStatus,omitempty"`

	// conditions represent the current state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Tiers",type="integer",JSONPath=".status.tierCount",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ModelTierConfig is the Schema for the modeltierconfigs API.
// It is cluster-scoped and maps abstract model tiers (fast/standard/reasoning)
// to specific LLM provider endpoints and authentication.
type ModelTierConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec   ModelTierConfigSpec   `json:"spec"`
	Status ModelTierConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ModelTierConfigList contains a list of ModelTierConfig.
type ModelTierConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelTierConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ModelTierConfig{}, &ModelTierConfigList{})
}
