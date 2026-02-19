/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// --- kubectl.get ---

// KubectlGetTool reads Kubernetes resources via the API.
type KubectlGetTool struct {
	clientset     kubernetes.Interface
	dynamicClient dynamic.Interface
}

func NewKubectlGetTool(cs kubernetes.Interface, dc dynamic.Interface) *KubectlGetTool {
	return &KubectlGetTool{clientset: cs, dynamicClient: dc}
}

func (t *KubectlGetTool) Name() string { return "kubectl.get" }

func (t *KubectlGetTool) Description() string {
	return "Get Kubernetes resources. Returns resource details as formatted text."
}

func (t *KubectlGetTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"resource": map[string]interface{}{
				"type":        "string",
				"description": "Resource type (e.g. pods, deployments, services)",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Resource name (omit for list)",
			},
			"namespace": map[string]interface{}{
				"type":        "string",
				"description": "Namespace (omit for cluster-scoped or all namespaces)",
			},
			"allNamespaces": map[string]interface{}{
				"type":        "boolean",
				"description": "List across all namespaces",
			},
			"labelSelector": map[string]interface{}{
				"type":        "string",
				"description": "Label selector filter",
			},
		},
		"required": []string{"resource"},
	}
}

func (t *KubectlGetTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, _ := args["resource"].(string)
	name, _ := args["name"].(string)
	namespace, _ := args["namespace"].(string)
	allNamespaces, _ := args["allNamespaces"].(bool)
	labelSelector, _ := args["labelSelector"].(string)

	if resource == "" {
		return "", fmt.Errorf("resource type is required")
	}

	gvr := resourceToGVR(resource)

	opts := metav1.ListOptions{}
	if labelSelector != "" {
		opts.LabelSelector = labelSelector
	}

	var buf bytes.Buffer

	if name != "" {
		// Get single resource
		obj, err := t.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("get %s/%s in %s: %w", resource, name, namespace, err)
		}
		formatResource(&buf, obj)
	} else if allNamespaces {
		list, err := t.dynamicClient.Resource(gvr).Namespace("").List(ctx, opts)
		if err != nil {
			return "", fmt.Errorf("list %s (all namespaces): %w", resource, err)
		}
		formatResourceList(&buf, resource, list)
	} else {
		list, err := t.dynamicClient.Resource(gvr).Namespace(namespace).List(ctx, opts)
		if err != nil {
			return "", fmt.Errorf("list %s in %s: %w", resource, namespace, err)
		}
		formatResourceList(&buf, resource, list)
	}

	return buf.String(), nil
}

// --- kubectl.describe ---

// KubectlDescribeTool provides detailed resource descriptions.
type KubectlDescribeTool struct {
	dynamicClient dynamic.Interface
}

func NewKubectlDescribeTool(dc dynamic.Interface) *KubectlDescribeTool {
	return &KubectlDescribeTool{dynamicClient: dc}
}

func (t *KubectlDescribeTool) Name() string { return "kubectl.describe" }

func (t *KubectlDescribeTool) Description() string {
	return "Describe a Kubernetes resource in detail, including events and conditions."
}

func (t *KubectlDescribeTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"resource":  map[string]interface{}{"type": "string", "description": "Resource type"},
			"name":      map[string]interface{}{"type": "string", "description": "Resource name"},
			"namespace": map[string]interface{}{"type": "string", "description": "Namespace"},
		},
		"required": []string{"resource", "name"},
	}
}

func (t *KubectlDescribeTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, _ := args["resource"].(string)
	name, _ := args["name"].(string)
	namespace, _ := args["namespace"].(string)

	gvr := resourceToGVR(resource)

	obj, err := t.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("describe %s/%s: %w", resource, name, err)
	}

	var buf bytes.Buffer
	formatResourceDetailed(&buf, obj)
	return buf.String(), nil
}

// --- kubectl.logs ---

// KubectlLogsTool reads pod logs.
type KubectlLogsTool struct {
	clientset kubernetes.Interface
}

func NewKubectlLogsTool(cs kubernetes.Interface) *KubectlLogsTool {
	return &KubectlLogsTool{clientset: cs}
}

func (t *KubectlLogsTool) Name() string { return "kubectl.logs" }

func (t *KubectlLogsTool) Description() string {
	return "Get logs from a pod container."
}

func (t *KubectlLogsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name":      map[string]interface{}{"type": "string", "description": "Pod name"},
			"namespace": map[string]interface{}{"type": "string", "description": "Namespace"},
			"container": map[string]interface{}{"type": "string", "description": "Container name (optional)"},
			"tailLines": map[string]interface{}{"type": "integer", "description": "Number of lines from end (default 100)"},
			"previous":  map[string]interface{}{"type": "boolean", "description": "Get previous container logs"},
		},
		"required": []string{"name", "namespace"},
	}
}

func (t *KubectlLogsTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	name, _ := args["name"].(string)
	namespace, _ := args["namespace"].(string)
	container, _ := args["container"].(string)
	previous, _ := args["previous"].(bool)

	tailLines := int64(100)
	if tl, ok := args["tailLines"].(float64); ok {
		tailLines = int64(tl)
	}

	opts := &corev1.PodLogOptions{
		TailLines: &tailLines,
		Previous:  previous,
	}
	if container != "" {
		opts.Container = container
	}

	stream, err := t.clientset.CoreV1().Pods(namespace).GetLogs(name, opts).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("get logs for %s/%s: %w", namespace, name, err)
	}
	defer stream.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(stream); err != nil {
		return "", fmt.Errorf("read log stream: %w", err)
	}

	return buf.String(), nil
}

// --- kubectl.apply ---

// KubectlApplyTool applies a resource manifest.
type KubectlApplyTool struct {
	dynamicClient dynamic.Interface
}

func NewKubectlApplyTool(dc dynamic.Interface) *KubectlApplyTool {
	return &KubectlApplyTool{dynamicClient: dc}
}

func (t *KubectlApplyTool) Name() string { return "kubectl.apply" }

func (t *KubectlApplyTool) Description() string {
	return "Apply (create or update) a Kubernetes resource from a YAML/JSON manifest."
}

func (t *KubectlApplyTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"manifest": map[string]interface{}{
				"type":        "string",
				"description": "YAML or JSON manifest to apply",
			},
			"namespace": map[string]interface{}{
				"type":        "string",
				"description": "Override namespace",
			},
		},
		"required": []string{"manifest"},
	}
}

func (t *KubectlApplyTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	// Implementation uses server-side apply via dynamic client
	// This is a stub — full implementation needs YAML parsing and SSA
	return "", fmt.Errorf("kubectl.apply: not yet implemented (Phase 2 stub)")
}

// --- kubectl.rollout ---

// KubectlRolloutTool manages rollouts.
type KubectlRolloutTool struct {
	dynamicClient dynamic.Interface
}

func NewKubectlRolloutTool(dc dynamic.Interface) *KubectlRolloutTool {
	return &KubectlRolloutTool{dynamicClient: dc}
}

func (t *KubectlRolloutTool) Name() string { return "kubectl.rollout" }

func (t *KubectlRolloutTool) Description() string {
	return "Manage rollouts: restart a deployment/statefulset."
}

func (t *KubectlRolloutTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":    map[string]interface{}{"type": "string", "description": "Rollout action: restart, status"},
			"resource":  map[string]interface{}{"type": "string", "description": "Resource type (deployment, statefulset)"},
			"name":      map[string]interface{}{"type": "string", "description": "Resource name"},
			"namespace": map[string]interface{}{"type": "string", "description": "Namespace"},
		},
		"required": []string{"action", "resource", "name", "namespace"},
	}
}

func (t *KubectlRolloutTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	action, _ := args["action"].(string)
	resource, _ := args["resource"].(string)
	name, _ := args["name"].(string)
	namespace, _ := args["namespace"].(string)

	gvr := resourceToGVR(resource)

	switch action {
	case "restart":
		// Rollout restart = patch with annotation
		obj, err := t.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("get %s/%s: %w", resource, name, err)
		}

		// Set restart annotation on pod template
		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations["kubectl.kubernetes.io/restartedAt"] = metav1.Now().Format("2006-01-02T15:04:05Z")
		obj.SetAnnotations(annotations)

		// Also set on pod template spec
		spec, _, _ := unstructured.NestedMap(obj.Object, "spec", "template", "metadata", "annotations")
		if spec == nil {
			spec = make(map[string]interface{})
		}
		spec["kubectl.kubernetes.io/restartedAt"] = metav1.Now().Format("2006-01-02T15:04:05Z")
		_ = unstructured.SetNestedField(obj.Object, spec, "spec", "template", "metadata", "annotations")

		_, err = t.dynamicClient.Resource(gvr).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{})
		if err != nil {
			return "", fmt.Errorf("rollout restart %s/%s: %w", resource, name, err)
		}
		return fmt.Sprintf("rollout restart triggered for %s/%s in %s", resource, name, namespace), nil

	case "status":
		obj, err := t.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("get %s/%s: %w", resource, name, err)
		}
		var buf bytes.Buffer
		formatResource(&buf, obj)
		return buf.String(), nil

	default:
		return "", fmt.Errorf("unknown rollout action: %s", action)
	}
}

// --- kubectl.scale ---

// KubectlScaleTool scales a workload.
type KubectlScaleTool struct {
	dynamicClient dynamic.Interface
}

func NewKubectlScaleTool(dc dynamic.Interface) *KubectlScaleTool {
	return &KubectlScaleTool{dynamicClient: dc}
}

func (t *KubectlScaleTool) Name() string { return "kubectl.scale" }

func (t *KubectlScaleTool) Description() string {
	return "Scale a deployment or statefulset to a given number of replicas."
}

func (t *KubectlScaleTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"resource":  map[string]interface{}{"type": "string", "description": "Resource type (deployment, statefulset)"},
			"name":      map[string]interface{}{"type": "string", "description": "Resource name"},
			"namespace": map[string]interface{}{"type": "string", "description": "Namespace"},
			"replicas":  map[string]interface{}{"type": "integer", "description": "Target replica count"},
		},
		"required": []string{"resource", "name", "namespace", "replicas"},
	}
}

func (t *KubectlScaleTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, _ := args["resource"].(string)
	name, _ := args["name"].(string)
	namespace, _ := args["namespace"].(string)
	replicas, _ := args["replicas"].(float64)

	gvr := resourceToGVR(resource)

	obj, err := t.dynamicClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get %s/%s: %w", resource, name, err)
	}

	err = unstructured.SetNestedField(obj.Object, int64(replicas), "spec", "replicas")
	if err != nil {
		return "", fmt.Errorf("set replicas: %w", err)
	}

	_, err = t.dynamicClient.Resource(gvr).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return "", fmt.Errorf("scale %s/%s: %w", resource, name, err)
	}

	return fmt.Sprintf("scaled %s/%s in %s to %d replicas", resource, name, namespace, int(replicas)), nil
}

// --- kubectl.delete ---

// KubectlDeleteTool deletes a Kubernetes resource.
// Note: The engine's data protection layer will block deletion of protected resources
// BEFORE this tool is ever called.
type KubectlDeleteTool struct {
	dynamicClient dynamic.Interface
}

func NewKubectlDeleteTool(dc dynamic.Interface) *KubectlDeleteTool {
	return &KubectlDeleteTool{dynamicClient: dc}
}

func (t *KubectlDeleteTool) Name() string { return "kubectl.delete" }

func (t *KubectlDeleteTool) Description() string {
	return "Delete a Kubernetes resource. WARNING: Destructive. Subject to guardrail enforcement."
}

func (t *KubectlDeleteTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"resource":  map[string]interface{}{"type": "string", "description": "Resource type"},
			"name":      map[string]interface{}{"type": "string", "description": "Resource name"},
			"namespace": map[string]interface{}{"type": "string", "description": "Namespace"},
		},
		"required": []string{"resource", "name"},
	}
}

func (t *KubectlDeleteTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	resource, _ := args["resource"].(string)
	name, _ := args["name"].(string)
	namespace, _ := args["namespace"].(string)

	gvr := resourceToGVR(resource)

	err := t.dynamicClient.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return "", fmt.Errorf("delete %s/%s: %w", resource, name, err)
	}

	return fmt.Sprintf("deleted %s/%s in %s", resource, name, namespace), nil
}

// --- Helpers ---

// resourceToGVR maps common resource names to GroupVersionResource.
func resourceToGVR(resource string) schema.GroupVersionResource {
	switch strings.ToLower(resource) {
	case "pods", "pod", "po":
		return schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	case "services", "service", "svc":
		return schema.GroupVersionResource{Version: "v1", Resource: "services"}
	case "deployments", "deployment", "deploy":
		return schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	case "statefulsets", "statefulset", "sts":
		return schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	case "daemonsets", "daemonset", "ds":
		return schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
	case "configmaps", "configmap", "cm":
		return schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	case "secrets", "secret":
		return schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	case "namespaces", "namespace", "ns":
		return schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	case "nodes", "node", "no":
		return schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	case "persistentvolumeclaims", "persistentvolumeclaim", "pvc":
		return schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	case "persistentvolumes", "persistentvolume", "pv":
		return schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}
	case "events", "event", "ev":
		return schema.GroupVersionResource{Version: "v1", Resource: "events"}
	case "ingresses", "ingress", "ing":
		return schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
	case "jobs", "job":
		return schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	case "cronjobs", "cronjob", "cj":
		return schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	case "replicasets", "replicaset", "rs":
		return schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	default:
		// Fallback: assume core group
		return schema.GroupVersionResource{Version: "v1", Resource: strings.ToLower(resource)}
	}
}

// formatResource writes a single resource summary.
func formatResource(buf *bytes.Buffer, obj *unstructured.Unstructured) {
	fmt.Fprintf(buf, "Name: %s\n", obj.GetName())
	fmt.Fprintf(buf, "Namespace: %s\n", obj.GetNamespace())
	fmt.Fprintf(buf, "Kind: %s\n", obj.GetKind())
	fmt.Fprintf(buf, "Created: %s\n", obj.GetCreationTimestamp().Format("2006-01-02T15:04:05Z"))

	if labels := obj.GetLabels(); len(labels) > 0 {
		fmt.Fprintf(buf, "Labels: %v\n", labels)
	}

	// Status summary if present
	if status, ok := obj.Object["status"]; ok {
		if statusMap, ok := status.(map[string]interface{}); ok {
			if phase, ok := statusMap["phase"].(string); ok {
				fmt.Fprintf(buf, "Phase: %s\n", phase)
			}
			if replicas, ok := statusMap["readyReplicas"]; ok {
				fmt.Fprintf(buf, "Ready Replicas: %v\n", replicas)
			}
			if conditions, ok := statusMap["conditions"].([]interface{}); ok {
				fmt.Fprintf(buf, "Conditions:\n")
				for _, c := range conditions {
					if cm, ok := c.(map[string]interface{}); ok {
						fmt.Fprintf(buf, "  - %s: %s (%s)\n",
							cm["type"], cm["status"], cm["reason"])
					}
				}
			}
		}
	}
}

// formatResourceList writes a list of resources as a table.
func formatResourceList(buf *bytes.Buffer, resource string, list *unstructured.UnstructuredList) {
	fmt.Fprintf(buf, "Found %d %s:\n", len(list.Items), resource)
	for _, obj := range list.Items {
		ns := obj.GetNamespace()
		if ns != "" {
			fmt.Fprintf(buf, "- %s/%s", ns, obj.GetName())
		} else {
			fmt.Fprintf(buf, "- %s", obj.GetName())
		}
		// Add quick status
		if status, ok := obj.Object["status"].(map[string]interface{}); ok {
			if phase, ok := status["phase"].(string); ok {
				fmt.Fprintf(buf, " (%s)", phase)
			}
		}
		buf.WriteString("\n")
	}
}

// formatResourceDetailed writes a detailed resource description.
func formatResourceDetailed(buf *bytes.Buffer, obj *unstructured.Unstructured) {
	formatResource(buf, obj)

	// Spec summary
	if spec, ok := obj.Object["spec"]; ok {
		fmt.Fprintf(buf, "\nSpec:\n")
		formatNested(buf, spec, "  ")
	}
}

// formatNested writes nested map/slice structures with indentation.
func formatNested(buf *bytes.Buffer, v interface{}, indent string) {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, v := range val {
			fmt.Fprintf(buf, "%s%s: ", indent, k)
			switch inner := v.(type) {
			case map[string]interface{}, []interface{}:
				buf.WriteString("\n")
				formatNested(buf, inner, indent+"  ")
			default:
				fmt.Fprintf(buf, "%v\n", inner)
			}
		}
	case []interface{}:
		for i, item := range val {
			fmt.Fprintf(buf, "%s[%d]:\n", indent, i)
			formatNested(buf, item, indent+"  ")
		}
	default:
		fmt.Fprintf(buf, "%s%v\n", indent, val)
	}
}
