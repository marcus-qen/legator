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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
)

// ResolvedEnvironment is an AgentEnvironment with all secrets resolved.
type ResolvedEnvironment struct {
	// Name is the environment name.
	Name string

	// Endpoints maps named endpoints to their specs.
	Endpoints map[string]corev1alpha1.EndpointSpec

	// Namespaces is the namespace map.
	Namespaces *corev1alpha1.NamespaceMap

	// Credentials maps named credentials to resolved values.
	// Keys are credential names, values are the resolved secret data.
	Credentials map[string]map[string]string

	// Channels maps named notification channels.
	Channels map[string]corev1alpha1.ChannelSpec

	// DataResources is the data protection configuration.
	DataResources *corev1alpha1.DataResourcesSpec

	// MCPServers maps named MCP tool servers.
	MCPServers map[string]corev1alpha1.MCPServerSpec

	// DataIndex is the pre-built index for fast data resource lookups.
	DataIndex *DataResourceIndex
}

// DataResourceIndex provides O(1) lookups for data resource membership.
type DataResourceIndex struct {
	// namespacesWithData contains namespaces that hold data resources.
	namespacesWithData map[string]bool

	// dataResourceKeys contains "kind/namespace/name" keys for direct lookups.
	dataResourceKeys map[string]bool

	// objectStorageNames contains object storage bucket names.
	objectStorageNames map[string]bool
}

// HasDataInNamespace checks if a namespace contains declared data resources.
func (idx *DataResourceIndex) HasDataInNamespace(namespace string) bool {
	return idx.namespacesWithData[namespace]
}

// IsDataResource checks if a specific resource is a declared data resource.
func (idx *DataResourceIndex) IsDataResource(kind, namespace, name string) bool {
	key := fmt.Sprintf("%s/%s/%s", kind, namespace, name)
	return idx.dataResourceKeys[key]
}

// IsProtectedObjectStorage checks if a bucket name is declared.
func (idx *DataResourceIndex) IsProtectedObjectStorage(name string) bool {
	return idx.objectStorageNames[name]
}

// EnvironmentResolver resolves AgentEnvironment CRs and their secret references.
type EnvironmentResolver struct {
	client    client.Client
	namespace string
}

// NewEnvironmentResolver creates a resolver for the given namespace.
func NewEnvironmentResolver(c client.Client, namespace string) *EnvironmentResolver {
	return &EnvironmentResolver{client: c, namespace: namespace}
}

// Resolve loads an AgentEnvironment by name and resolves all secret references.
func (r *EnvironmentResolver) Resolve(ctx context.Context, envName string) (*ResolvedEnvironment, error) {
	env := &corev1alpha1.AgentEnvironment{}
	if err := r.client.Get(ctx, types.NamespacedName{
		Name:      envName,
		Namespace: r.namespace,
	}, env); err != nil {
		return nil, fmt.Errorf("failed to get AgentEnvironment %q: %w", envName, err)
	}

	resolved := &ResolvedEnvironment{
		Name:          env.Name,
		Endpoints:     env.Spec.Endpoints,
		Namespaces:    env.Spec.Namespaces,
		Channels:      env.Spec.Channels,
		DataResources: env.Spec.DataResources,
		MCPServers:    env.Spec.MCPServers,
	}

	// Resolve credentials
	if len(env.Spec.Credentials) > 0 {
		resolved.Credentials = make(map[string]map[string]string)
		for name, cred := range env.Spec.Credentials {
			secretData, err := r.resolveSecret(ctx, cred.SecretRef)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve credential %q (secret %q): %w",
					name, cred.SecretRef, err)
			}
			resolved.Credentials[name] = secretData
		}
	}

	// Build data resource index
	resolved.DataIndex = buildDataIndex(env.Spec.DataResources)

	return resolved, nil
}

// resolveSecret reads a Secret and returns its data as string map.
func (r *EnvironmentResolver) resolveSecret(ctx context.Context, secretName string) (map[string]string, error) {
	secret := &corev1.Secret{}
	if err := r.client.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: r.namespace,
	}, secret); err != nil {
		return nil, err
	}

	data := make(map[string]string)
	for k, v := range secret.Data {
		data[k] = string(v)
	}
	return data, nil
}

// buildDataIndex creates a fast lookup index from DataResourcesSpec.
func buildDataIndex(dr *corev1alpha1.DataResourcesSpec) *DataResourceIndex {
	idx := &DataResourceIndex{
		namespacesWithData: make(map[string]bool),
		dataResourceKeys:   make(map[string]bool),
		objectStorageNames: make(map[string]bool),
	}

	if dr == nil {
		return idx
	}

	for _, db := range dr.Databases {
		idx.namespacesWithData[db.Namespace] = true
		key := fmt.Sprintf("%s/%s/%s", db.Kind, db.Namespace, db.Name)
		idx.dataResourceKeys[key] = true
	}

	for _, ps := range dr.PersistentStorage {
		idx.namespacesWithData[ps.Namespace] = true
		key := fmt.Sprintf("%s/%s/%s", ps.Kind, ps.Namespace, ps.Name)
		idx.dataResourceKeys[key] = true
	}

	for _, os := range dr.ObjectStorage {
		idx.objectStorageNames[os.Name] = true
	}

	return idx
}
