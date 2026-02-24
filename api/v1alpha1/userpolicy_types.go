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

// UserPolicyRole is the RBAC role granted to matching subjects.
// +kubebuilder:validation:Enum=viewer;operator;admin
type UserPolicyRole string

const (
	UserPolicyRoleViewer   UserPolicyRole = "viewer"
	UserPolicyRoleOperator UserPolicyRole = "operator"
	UserPolicyRoleAdmin    UserPolicyRole = "admin"
)

// UserPolicyMaxAutonomy caps the maximum autonomy level a user can request.
// +kubebuilder:validation:Enum=observe;recommend;automate-safe;automate-destructive
type UserPolicyMaxAutonomy string

const (
	UserPolicyAutonomyObserve             UserPolicyMaxAutonomy = "observe"
	UserPolicyAutonomyRecommend           UserPolicyMaxAutonomy = "recommend"
	UserPolicyAutonomyAutomateSafe        UserPolicyMaxAutonomy = "automate-safe"
	UserPolicyAutonomyAutomateDestructive UserPolicyMaxAutonomy = "automate-destructive"
)

// UserPolicySubject matches identities via OIDC claims.
type UserPolicySubject struct {
	// claim is the OIDC claim to match.
	// Supported values: email, sub, groups, name.
	// +required
	// +kubebuilder:validation:Enum=email;sub;groups;name
	Claim string `json:"claim"`

	// value is the expected claim value.
	// Supports exact values and suffix wildcard globs (e.g. team-*).
	// +required
	Value string `json:"value"`
}

// UserPolicyScope constrains where a policy grant may apply.
type UserPolicyScope struct {
	// tags limits to resources tagged with any of these values.
	// Empty means no tag restriction.
	// +optional
	Tags []string `json:"tags,omitempty"`

	// namespaces limits to these Kubernetes namespaces.
	// Empty means no namespace restriction.
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// agents limits to matching agent names.
	// Supports suffix wildcard globs.
	// +optional
	Agents []string `json:"agents,omitempty"`

	// maxAutonomy caps the maximum autonomy level available to this policy.
	// +optional
	MaxAutonomy UserPolicyMaxAutonomy `json:"maxAutonomy,omitempty"`
}

// UserPolicySpec defines user-scoped authorization policy.
type UserPolicySpec struct {
	// description is a human-readable summary of what this policy grants.
	// +optional
	Description string `json:"description,omitempty"`

	// subjects are the identities this policy applies to.
	// +required
	// +kubebuilder:validation:MinItems=1
	Subjects []UserPolicySubject `json:"subjects"`

	// role granted when a subject matches.
	// +required
	Role UserPolicyRole `json:"role"`

	// scope constrains where the grant is effective.
	// +optional
	Scope UserPolicyScope `json:"scope,omitempty"`
}

// UserPolicyStatus captures validation and readiness state.
type UserPolicyStatus struct {
	// ready indicates whether the policy is valid and usable.
	// +optional
	Ready bool `json:"ready,omitempty"`

	// observedGeneration is the generation last processed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// effectiveRole is the reconciled role value.
	// +optional
	EffectiveRole UserPolicyRole `json:"effectiveRole,omitempty"`

	// matchedSubjects is the number of configured subject matchers.
	// +optional
	MatchedSubjects int32 `json:"matchedSubjects,omitempty"`

	// validationErrors contains schema-level validation failures detected by reconciliation.
	// +optional
	ValidationErrors []string `json:"validationErrors,omitempty"`

	// conditions represent current reconciliation state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=upol
// +kubebuilder:printcolumn:name="Role",type="string",JSONPath=".spec.role"
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Subjects",type="integer",JSONPath=".status.matchedSubjects",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// UserPolicy is the schema for user-scoped authorization policy overrides.
type UserPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec UserPolicySpec `json:"spec"`

	// +optional
	Status UserPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// UserPolicyList contains a list of UserPolicy.
type UserPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []UserPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&UserPolicy{}, &UserPolicyList{})
}
