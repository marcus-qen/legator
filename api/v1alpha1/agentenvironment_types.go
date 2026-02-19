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

// --- Connection types ---

// ConnectionSpec defines how the agent connects to its target cluster.
type ConnectionSpec struct {
	// kind is the connection type.
	// +required
	// +kubebuilder:default="in-cluster"
	// +kubebuilder:validation:Enum="in-cluster";"kubeconfig"
	Kind string `json:"kind"`

	// kubeconfig references a Secret containing a kubeconfig for remote clusters.
	// +optional
	Kubeconfig *KubeconfigRef `json:"kubeconfig,omitempty"`
}

// KubeconfigRef points to a Secret holding a kubeconfig.
type KubeconfigRef struct {
	// secretRef is the name of the Secret in the agent's namespace.
	// +required
	SecretRef string `json:"secretRef"`

	// key is the data key within the Secret (default "kubeconfig").
	// +optional
	// +kubebuilder:default="kubeconfig"
	Key string `json:"key,omitempty"`
}

// --- Endpoint types ---

// EndpointSpec defines a named endpoint the agent can use.
type EndpointSpec struct {
	// url is the base URL.
	// +required
	URL string `json:"url"`

	// healthPath is appended to URL for health checks.
	// +optional
	HealthPath string `json:"healthPath,omitempty"`

	// internal marks this as a cluster-internal endpoint (no TLS required).
	// +optional
	Internal bool `json:"internal,omitempty"`
}

// --- Credential types ---

// CredentialRef references a Secret for tool authentication.
type CredentialRef struct {
	// secretRef is the name of the Secret.
	// +required
	SecretRef string `json:"secretRef"`

	// type indicates the credential format.
	// +required
	// +kubebuilder:validation:Enum="bearer-token";"token";"api-key";"basic-auth";"tls"
	Type string `json:"type"`
}

// --- Channel types ---

// ChannelSpec defines a notification channel.
type ChannelSpec struct {
	// type is the channel provider.
	// +required
	// +kubebuilder:validation:Enum="slack";"telegram";"webhook";"agent"
	Type string `json:"type"`

	// target is the channel destination (webhook URL, chat ID, agent name, etc.).
	// +required
	Target string `json:"target"`

	// secretRef optionally references auth credentials for the channel.
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
}

// --- Data Resources ---

// DataResourcesSpec declares all data resources in this environment.
// The runtime uses this to automatically classify operations as data-mutations
// and enforce data protection rules.
type DataResourcesSpec struct {
	// backupMaxAge is the max acceptable time since last backup.
	// Operations on data-adjacent resources warn if backup is older than this.
	// +optional
	// +kubebuilder:default="24h"
	BackupMaxAge string `json:"backupMaxAge,omitempty"`

	// databases lists database cluster CRs.
	// +optional
	Databases []DataResourceRef `json:"databases,omitempty"`

	// persistentStorage lists PVCs and PVs.
	// +optional
	PersistentStorage []DataResourceRef `json:"persistentStorage,omitempty"`

	// objectStorage lists S3/MinIO buckets.
	// +optional
	ObjectStorage []ObjectStorageRef `json:"objectStorage,omitempty"`
}

// DataResourceRef identifies a data-bearing Kubernetes resource.
type DataResourceRef struct {
	// kind is the resource kind (e.g. "cnpg.io/Cluster", "PersistentVolumeClaim").
	// +required
	Kind string `json:"kind"`

	// namespace is where the resource lives.
	// +required
	Namespace string `json:"namespace"`

	// name is the resource name.
	// +required
	Name string `json:"name"`

	// backupSchedule is informational — helps the runtime check backup freshness.
	// +optional
	BackupSchedule string `json:"backupSchedule,omitempty"`
}

// ObjectStorageRef identifies an object storage resource.
type ObjectStorageRef struct {
	// kind describes the storage type (e.g. "s3-bucket").
	// +required
	Kind string `json:"kind"`

	// name is the bucket name.
	// +required
	Name string `json:"name"`

	// provider identifies the storage provider (e.g. "minio", "aws").
	// +optional
	Provider string `json:"provider,omitempty"`
}

// --- MCP Server ---

// MCPServerSpec defines an available MCP tool server.
type MCPServerSpec struct {
	// endpoint is the URL of the MCP server.
	// +required
	Endpoint string `json:"endpoint"`

	// capabilities lists the tool capabilities this server provides.
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`
}

// --- Namespace Map ---

// NamespaceMap groups namespaces by role.
type NamespaceMap struct {
	// monitoring namespaces.
	// +optional
	Monitoring []string `json:"monitoring,omitempty"`

	// apps are application namespaces the agent can access.
	// +optional
	Apps []string `json:"apps,omitempty"`

	// system are platform/infrastructure namespaces.
	// +optional
	System []string `json:"system,omitempty"`

	// additional allows arbitrary namespace groupings.
	// +optional
	Additional map[string][]string `json:"additional,omitempty"`
}

// --- Main spec ---

// AgentEnvironmentSpec defines the site-specific configuration for an agent.
type AgentEnvironmentSpec struct {
	// connection defines how the agent connects to its target cluster.
	// +optional
	Connection *ConnectionSpec `json:"connection,omitempty"`

	// endpoints maps named endpoints to their URLs and health paths.
	// +optional
	Endpoints map[string]EndpointSpec `json:"endpoints,omitempty"`

	// namespaces groups cluster namespaces by role.
	// +optional
	Namespaces *NamespaceMap `json:"namespaces,omitempty"`

	// credentials maps named credentials to Secret references.
	// +optional
	Credentials map[string]CredentialRef `json:"credentials,omitempty"`

	// channels maps named notification channels.
	// +optional
	Channels map[string]ChannelSpec `json:"channels,omitempty"`

	// dataResources declares all data resources in this environment.
	// +optional
	DataResources *DataResourcesSpec `json:"dataResources,omitempty"`

	// mcpServers maps named MCP tool servers.
	// +optional
	MCPServers map[string]MCPServerSpec `json:"mcpServers,omitempty"`
}

// AgentEnvironmentPhase represents the lifecycle phase.
// +kubebuilder:validation:Enum=Pending;Ready;Error
type AgentEnvironmentPhase string

const (
	AgentEnvironmentPhasePending AgentEnvironmentPhase = "Pending"
	AgentEnvironmentPhaseReady   AgentEnvironmentPhase = "Ready"
	AgentEnvironmentPhaseError   AgentEnvironmentPhase = "Error"
)

// AgentEnvironmentStatus defines the observed state of AgentEnvironment.
type AgentEnvironmentStatus struct {
	// phase is the current lifecycle phase.
	// +optional
	Phase AgentEnvironmentPhase `json:"phase,omitempty"`

	// referencedBy lists agents bound to this environment.
	// +optional
	ReferencedBy []string `json:"referencedBy,omitempty"`

	// conditions represent the current state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Endpoints",type="integer",JSONPath=".status.endpointCount",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// AgentEnvironment is the Schema for the agentenvironments API.
// It defines the site-specific configuration — endpoints, credentials,
// namespaces, notification channels, data resources, and MCP servers —
// that an InfraAgent binds to at runtime.
type AgentEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec   AgentEnvironmentSpec   `json:"spec"`
	Status AgentEnvironmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentEnvironmentList contains a list of AgentEnvironment.
type AgentEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentEnvironment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentEnvironment{}, &AgentEnvironmentList{})
}
