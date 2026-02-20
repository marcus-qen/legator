/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentStateSpec defines the desired state for an agent's persistent memory.
type AgentStateSpec struct {
	// agentName is the agent this state belongs to.
	AgentName string `json:"agentName"`

	// maxKeys is the maximum number of keys allowed (default: 100).
	// +optional
	MaxKeys int `json:"maxKeys,omitempty"`

	// maxValueSize is the maximum size of a single value in bytes (default: 4096).
	// +optional
	MaxValueSize int `json:"maxValueSize,omitempty"`
}

// AgentStateStatus holds the actual state data.
type AgentStateStatus struct {
	// entries is the key-value store.
	// +optional
	Entries map[string]StateEntry `json:"entries,omitempty"`

	// totalSize is the approximate total size of all entries in bytes.
	// +optional
	TotalSize int `json:"totalSize,omitempty"`

	// lastUpdated is the last time any entry was modified.
	// +optional
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`
}

// StateEntry is a single key-value entry with metadata.
type StateEntry struct {
	// value is the stored data (string, JSON, etc.).
	Value string `json:"value"`

	// updatedAt is when this entry was last written.
	UpdatedAt metav1.Time `json:"updatedAt"`

	// updatedBy is the LegatorRun that last wrote this entry.
	// +optional
	UpdatedBy string `json:"updatedBy,omitempty"`

	// ttl is the optional time-to-live for this entry (e.g., "24h").
	// Expired entries are cleaned up by the controller.
	// +optional
	TTL string `json:"ttl,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=as
// +kubebuilder:printcolumn:name="Agent",type="string",JSONPath=".spec.agentName"
// +kubebuilder:printcolumn:name="Keys",type="integer",JSONPath=".status.totalSize"
// +kubebuilder:printcolumn:name="Updated",type="date",JSONPath=".status.lastUpdated"

// AgentState is the Schema for the agentstates API.
// Provides persistent key-value storage for agents between runs.
type AgentState struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentStateSpec   `json:"spec,omitempty"`
	Status AgentStateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentStateList contains a list of AgentState.
type AgentStateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentState `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentState{}, &AgentStateList{})
}
