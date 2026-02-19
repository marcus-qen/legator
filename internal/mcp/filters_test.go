/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mcp

import (
	"strings"
	"testing"
)

func TestK8sGPTNoiseFilterKubeRootCA(t *testing.T) {
	input := "ConfigMap default/kube-root-ca.crt is unused\nPod backstage/app is failing"
	result := K8sGPTNoiseFilter("k8sgpt", "analyze", input)

	if strings.Contains(result, "kube-root-ca.crt") {
		t.Error("kube-root-ca.crt should be filtered")
	}
	if !strings.Contains(result, "Pod backstage/app is failing") {
		t.Error("real issue should remain")
	}
}

func TestK8sGPTNoiseFilterKyverno(t *testing.T) {
	input := "Service backstage/app has Kyverno policy violation event\nStatefulSet tempo missing headless service"
	result := K8sGPTNoiseFilter("k8sgpt", "analyze", input)

	if strings.Contains(result, "Kyverno") {
		t.Error("Kyverno policy violation should be filtered")
	}
	if !strings.Contains(result, "tempo") {
		t.Error("real StatefulSet issue should remain")
	}
}

func TestK8sGPTNoiseFilterCNPGRO(t *testing.T) {
	input := "Service backstage/backstage-db-ro has no endpoints\nPod crashing in monitoring"
	result := K8sGPTNoiseFilter("k8sgpt", "analyze", input)

	if strings.Contains(result, "backstage-db-ro") {
		t.Error("CNPG read-only service with no endpoints should be filtered")
	}
	if !strings.Contains(result, "monitoring") {
		t.Error("real issue should remain")
	}
}

func TestK8sGPTNoiseFilterNonK8sGPT(t *testing.T) {
	input := "ConfigMap default/kube-root-ca.crt is unused"
	result := K8sGPTNoiseFilter("other-server", "analyze", input)

	// Should pass through unchanged for non-k8sgpt servers
	if result != input {
		t.Errorf("non-k8sgpt result should pass through unchanged, got %q", result)
	}
}

func TestK8sGPTNoiseFilterAllFiltered(t *testing.T) {
	input := "ConfigMap default/kube-root-ca.crt is unused\nConfigMap kube-system/kube-root-ca.crt is unused"
	result := K8sGPTNoiseFilter("k8sgpt", "analyze", input)

	if !strings.Contains(result, "no actionable findings") {
		t.Errorf("all-filtered result should indicate no actionable findings, got %q", result)
	}
}

func TestK8sGPTNoiseFilterEmpty(t *testing.T) {
	result := K8sGPTNoiseFilter("k8sgpt", "analyze", "")

	if !strings.Contains(result, "no actionable findings") {
		t.Errorf("empty input should indicate no actionable findings, got %q", result)
	}
}

func TestK8sGPTNoiseFilterRealIssuesPass(t *testing.T) {
	input := `Pod devspaces/lifecycle-job-xyz is failing with exitCode=2
StatefulSet monitoring/tempo references non-existent tempo-headless service
Deployment backstage/backstage has 0/1 replicas ready`

	result := K8sGPTNoiseFilter("k8sgpt", "analyze", input)

	// All real issues should pass through
	if !strings.Contains(result, "exitCode=2") {
		t.Error("exitCode=2 issue should pass through")
	}
	if !strings.Contains(result, "tempo-headless") {
		t.Error("tempo-headless issue should pass through")
	}
	if !strings.Contains(result, "0/1 replicas") {
		t.Error("replicas issue should pass through")
	}
}

func TestDefaultNoiseFilters(t *testing.T) {
	filters := DefaultNoiseFilters()
	if len(filters) == 0 {
		t.Error("DefaultNoiseFilters should return at least one filter")
	}
}

func TestShouldFilterK8sGPTLine(t *testing.T) {
	tests := []struct {
		line   string
		filter bool
	}{
		{"ConfigMap default/kube-root-ca.crt is unused", true},
		{"Service has Kyverno policy violation", true},
		{"Service backstage/backstage-db-ro has no endpoints", true},
		{"Pod backstage/app-xyz is CrashLoopBackOff", false},
		{"Deployment has 0 ready replicas", false},
		{"StatefulSet references missing service", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := shouldFilterK8sGPTLine(tt.line); got != tt.filter {
			t.Errorf("shouldFilterK8sGPTLine(%q) = %v, want %v", tt.line, got, tt.filter)
		}
	}
}
