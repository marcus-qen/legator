/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package approval

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
)

const typedConfirmationRequiredValue = "true"

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	corev1alpha1.AddToScheme(s)
	return s
}

func TestSanitizeLabel(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"kubectl.get", "kubectl-get"},
		{"ssh.exec", "ssh-exec"},
		{"simple", "simple"},
		{"a/b/c.d", "a-b-c-d"},
	}
	for _, tt := range tests {
		got := sanitizeLabel(tt.in)
		if got != tt.want {
			t.Errorf("sanitizeLabel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeLabel_Length(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "a"
	}
	got := sanitizeLabel(long)
	if len(got) > 63 {
		t.Errorf("sanitizeLabel should truncate to 63 chars, got %d", len(got))
	}
}

func TestRequiresTypedConfirmation(t *testing.T) {
	if RequiresTypedConfirmation(corev1alpha1.ActionTierRead) {
		t.Fatal("read should not require typed confirmation")
	}
	if RequiresTypedConfirmation(corev1alpha1.ActionTierServiceMutation) {
		t.Fatal("service-mutation should not require typed confirmation")
	}
	if !RequiresTypedConfirmation(corev1alpha1.ActionTierDestructiveMutation) {
		t.Fatal("destructive-mutation should require typed confirmation")
	}
	if !RequiresTypedConfirmation(corev1alpha1.ActionTierDataMutation) {
		t.Fatal("data-mutation should require typed confirmation")
	}
}

func TestValidateTypedConfirmation(t *testing.T) {
	now := time.Now()
	ar := &corev1alpha1.ApprovalRequest{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		AnnotationTypedConfirmationRequired:  typedConfirmationRequiredValue,
		AnnotationTypedConfirmationToken:     "CONFIRM-ABCD1234",
		AnnotationTypedConfirmationExpiresAt: now.Add(5 * time.Minute).Format(time.RFC3339),
	}}}

	if err := ValidateTypedConfirmation(ar, "", now); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected required error, got %v", err)
	}
	if err := ValidateTypedConfirmation(ar, "CONFIRM-WRONG", now); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected mismatch error, got %v", err)
	}
	if err := ValidateTypedConfirmation(ar, "CONFIRM-ABCD1234", now); err != nil {
		t.Fatalf("expected valid confirmation, got %v", err)
	}
	if err := ValidateTypedConfirmation(ar, "CONFIRM-ABCD1234", now.Add(10*time.Minute)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired error, got %v", err)
	}
}

func TestManager_RequestApproval_AddsTypedConfirmationMetadata(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1alpha1.ApprovalRequest{}).Build()

	mgr := NewManager(c, logr.Discard())
	mgr.pollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, _ = mgr.RequestApproval(ctx, ApprovalParams{
		AgentName: "test-agent",
		RunName:   "test-run-typed",
		Namespace: "default",
		Tool:      "kubectl.delete",
		Tier:      corev1alpha1.ActionTierDestructiveMutation,
		Target:    "deployment/api",
		Timeout:   "2m",
	})

	list := &corev1alpha1.ApprovalRequestList{}
	if err := c.List(context.Background(), list); err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(list.Items) == 0 {
		t.Fatal("expected created approval request")
	}
	ar := list.Items[0]
	if ar.Annotations[AnnotationTypedConfirmationRequired] != typedConfirmationRequiredValue {
		t.Fatalf("expected typed confirmation required annotation, got %q", ar.Annotations[AnnotationTypedConfirmationRequired])
	}
	if !strings.HasPrefix(ar.Annotations[AnnotationTypedConfirmationToken], "CONFIRM-") {
		t.Fatalf("expected typed confirmation token, got %q", ar.Annotations[AnnotationTypedConfirmationToken])
	}
	if strings.TrimSpace(ar.Annotations[AnnotationTypedConfirmationExpiresAt]) == "" {
		t.Fatal("expected typed confirmation expiry annotation")
	}
	if !strings.Contains(ar.Spec.Context, "Typed confirmation required") {
		t.Fatalf("expected approval context to include typed confirmation instruction, got: %q", ar.Spec.Context)
	}
}

func TestManager_RequestApproval_Approved(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1alpha1.ApprovalRequest{}).Build()

	mgr := NewManager(c, logr.Discard())
	mgr.pollInterval = 50 * time.Millisecond

	// Simulate approval in background
	go func() {
		time.Sleep(200 * time.Millisecond)
		// Find the created ApprovalRequest
		list := &corev1alpha1.ApprovalRequestList{}
		if err := c.List(context.Background(), list); err != nil {
			t.Errorf("list approvals: %v", err)
			return
		}
		if len(list.Items) == 0 {
			t.Error("no approvals found")
			return
		}
		ar := &list.Items[0]
		now := metav1.Now()
		ar.Status.Phase = corev1alpha1.ApprovalPhaseApproved
		ar.Status.DecidedBy = "test-user"
		ar.Status.DecidedAt = &now
		ar.Status.Reason = "looks good"
		if err := c.Status().Update(context.Background(), ar); err != nil {
			t.Errorf("approve: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := mgr.RequestApproval(ctx, ApprovalParams{
		AgentName: "test-agent",
		RunName:   "test-run-123",
		Namespace: "default",
		Tool:      "kubectl.apply",
		Tier:      corev1alpha1.ActionTierServiceMutation,
		Target:    "deployment/nginx",
		Timeout:   "1m",
	})

	if err != nil {
		t.Fatalf("RequestApproval error: %v", err)
	}
	if !result.Approved {
		t.Error("expected approved")
	}
	if result.Phase != corev1alpha1.ApprovalPhaseApproved {
		t.Errorf("phase = %q, want Approved", result.Phase)
	}
	if result.DecidedBy != "test-user" {
		t.Errorf("decidedBy = %q, want test-user", result.DecidedBy)
	}
}

func TestManager_RequestApproval_Denied(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1alpha1.ApprovalRequest{}).Build()

	mgr := NewManager(c, logr.Discard())
	mgr.pollInterval = 50 * time.Millisecond

	go func() {
		time.Sleep(200 * time.Millisecond)
		list := &corev1alpha1.ApprovalRequestList{}
		if err := c.List(context.Background(), list); err != nil {
			return
		}
		if len(list.Items) == 0 {
			return
		}
		ar := &list.Items[0]
		now := metav1.Now()
		ar.Status.Phase = corev1alpha1.ApprovalPhaseDenied
		ar.Status.DecidedBy = "security-team"
		ar.Status.DecidedAt = &now
		ar.Status.Reason = "too risky"
		c.Status().Update(context.Background(), ar)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := mgr.RequestApproval(ctx, ApprovalParams{
		AgentName: "test-agent",
		RunName:   "test-run-456",
		Namespace: "default",
		Tool:      "kubectl.delete",
		Tier:      corev1alpha1.ActionTierDestructiveMutation,
		Target:    "pod/critical-pod",
		Timeout:   "1m",
	})

	if err != nil {
		t.Fatalf("RequestApproval error: %v", err)
	}
	if result.Approved {
		t.Error("expected denied, got approved")
	}
	if result.Phase != corev1alpha1.ApprovalPhaseDenied {
		t.Errorf("phase = %q, want Denied", result.Phase)
	}
}

func TestManager_RequestApproval_Timeout(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1alpha1.ApprovalRequest{}).Build()

	mgr := NewManager(c, logr.Discard())
	mgr.pollInterval = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := mgr.RequestApproval(ctx, ApprovalParams{
		AgentName: "test-agent",
		RunName:   "test-run-789",
		Namespace: "default",
		Tool:      "ssh.exec",
		Tier:      corev1alpha1.ActionTierServiceMutation,
		Target:    "reboot",
		Timeout:   "200ms", // very short timeout
	})

	if err != nil {
		t.Fatalf("RequestApproval error: %v", err)
	}
	if result.Approved {
		t.Error("expected not approved on timeout")
	}
	if result.Phase != corev1alpha1.ApprovalPhaseExpired {
		t.Errorf("phase = %q, want Expired", result.Phase)
	}
}

func TestManager_RequestApproval_ContextCancelled(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1alpha1.ApprovalRequest{}).Build()

	mgr := NewManager(c, logr.Discard())
	mgr.pollInterval = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	result, err := mgr.RequestApproval(ctx, ApprovalParams{
		AgentName: "test-agent",
		RunName:   "test-run-cancel",
		Namespace: "default",
		Tool:      "kubectl.apply",
		Tier:      corev1alpha1.ActionTierServiceMutation,
		Target:    "deployment/app",
		Timeout:   "10m",
	})

	if err == nil {
		t.Fatal("expected context error")
	}
	if result.Phase != corev1alpha1.ApprovalPhaseExpired {
		t.Errorf("phase = %q, want Expired", result.Phase)
	}
}
