/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package events

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	corev1alpha1.AddToScheme(s)
	return s
}

func TestBus_Publish(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1alpha1.AgentEvent{}).Build()
	bus := NewBus(c, logr.Discard())

	ctx := context.Background()
	event, err := bus.Publish(ctx, PublishParams{
		SourceAgent: "watchman-light",
		Namespace:   "agents",
		EventType:   "pod-crash-loop",
		Severity:    corev1alpha1.EventSeverityCritical,
		Summary:     "Pod backstage-dev-abc is CrashLoopBackOff",
		Detail:      "OOMKilled 3 times in 10 minutes",
		TTL:         "1h",
	})

	if err != nil {
		t.Fatalf("Publish error: %v", err)
	}
	if event.Name == "" {
		t.Error("expected event name to be generated")
	}
	if event.Spec.SourceAgent != "watchman-light" {
		t.Errorf("source = %q, want watchman-light", event.Spec.SourceAgent)
	}
	if event.Status.Phase != corev1alpha1.EventPhaseNew {
		t.Errorf("phase = %q, want New", event.Status.Phase)
	}
}

func TestBus_FindNewEvents(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1alpha1.AgentEvent{}).Build()
	bus := NewBus(c, logr.Discard())

	ctx := context.Background()

	// Publish two events
	bus.Publish(ctx, PublishParams{
		SourceAgent: "watchman-light",
		Namespace:   "agents",
		EventType:   "pod-crash-loop",
		Severity:    corev1alpha1.EventSeverityCritical,
		Summary:     "Critical pod crash",
		TTL:         "1h",
	})
	bus.Publish(ctx, PublishParams{
		SourceAgent: "watchman-light",
		Namespace:   "agents",
		EventType:   "disk-pressure",
		Severity:    corev1alpha1.EventSeverityWarning,
		Summary:     "Node disk at 90%",
		TTL:         "1h",
	})

	// Find all events for tribune
	events, err := bus.FindNewEvents(ctx, SubscribeParams{
		Namespace:     "agents",
		ConsumerAgent: "tribune",
	})
	if err != nil {
		t.Fatalf("FindNewEvents error: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("found %d events, want 2", len(events))
	}
}

func TestBus_FindNewEvents_SeverityFilter(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1alpha1.AgentEvent{}).Build()
	bus := NewBus(c, logr.Discard())

	ctx := context.Background()

	bus.Publish(ctx, PublishParams{
		SourceAgent: "watchman-light",
		Namespace:   "agents",
		EventType:   "pod-crash-loop",
		Severity:    corev1alpha1.EventSeverityCritical,
		Summary:     "Critical event",
		TTL:         "1h",
	})
	bus.Publish(ctx, PublishParams{
		SourceAgent: "watchman-light",
		Namespace:   "agents",
		EventType:   "info-event",
		Severity:    corev1alpha1.EventSeverityInfo,
		Summary:     "Just info",
		TTL:         "1h",
	})

	// Only critical+warning
	events, err := bus.FindNewEvents(ctx, SubscribeParams{
		Namespace:     "agents",
		ConsumerAgent: "forge",
		MinSeverity:   corev1alpha1.EventSeverityWarning,
	})
	if err != nil {
		t.Fatalf("FindNewEvents error: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("found %d events, want 1 (critical only)", len(events))
	}
}

func TestBus_FindNewEvents_AlreadyConsumed(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1alpha1.AgentEvent{}).Build()
	bus := NewBus(c, logr.Discard())

	ctx := context.Background()

	event, _ := bus.Publish(ctx, PublishParams{
		SourceAgent: "watchman-light",
		Namespace:   "agents",
		EventType:   "finding",
		Severity:    corev1alpha1.EventSeverityCritical,
		Summary:     "Something found",
		TTL:         "1h",
	})

	// Consume it
	bus.Consume(ctx, event.Name, "agents", "tribune", "tribune-run-123")

	// Should not show up again for tribune
	events, _ := bus.FindNewEvents(ctx, SubscribeParams{
		Namespace:     "agents",
		ConsumerAgent: "tribune",
	})
	if len(events) != 0 {
		t.Errorf("found %d events, want 0 (already consumed)", len(events))
	}

	// Should still show for forge
	events, _ = bus.FindNewEvents(ctx, SubscribeParams{
		Namespace:     "agents",
		ConsumerAgent: "forge",
	})
	if len(events) != 1 {
		t.Errorf("found %d events for forge, want 1", len(events))
	}
}

func TestBus_FindNewEvents_TargetAgent(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1alpha1.AgentEvent{}).Build()
	bus := NewBus(c, logr.Discard())

	ctx := context.Background()

	// Targeted event
	bus.Publish(ctx, PublishParams{
		SourceAgent: "watchman-light",
		Namespace:   "agents",
		EventType:   "remediation-needed",
		Severity:    corev1alpha1.EventSeverityCritical,
		Summary:     "Fix this",
		TargetAgent: "forge",
		TTL:         "1h",
	})

	// Tribune should not see it
	events, _ := bus.FindNewEvents(ctx, SubscribeParams{
		Namespace:     "agents",
		ConsumerAgent: "tribune",
	})
	if len(events) != 0 {
		t.Errorf("tribune found %d events, want 0 (targeted to forge)", len(events))
	}

	// Forge should see it
	events, _ = bus.FindNewEvents(ctx, SubscribeParams{
		Namespace:     "agents",
		ConsumerAgent: "forge",
	})
	if len(events) != 1 {
		t.Errorf("forge found %d events, want 1", len(events))
	}
}

func TestBus_CleanExpired(t *testing.T) {
	scheme := newScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&corev1alpha1.AgentEvent{}).Build()
	bus := NewBus(c, logr.Discard())

	ctx := context.Background()

	// Create an event with past creation time (simulating expired)
	event := &corev1alpha1.AgentEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "old-event",
			Namespace:         "agents",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
		},
		Spec: corev1alpha1.AgentEventSpec{
			SourceAgent: "test",
			EventType:   "test",
			Severity:    corev1alpha1.EventSeverityInfo,
			Summary:     "old event",
			TTL:         "1h",
		},
	}
	c.Create(ctx, event)

	deleted, err := bus.CleanExpired(ctx, "agents", 24*time.Hour)
	if err != nil {
		t.Fatalf("CleanExpired error: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted %d, want 1", deleted)
	}
}

func TestSeverityMeets(t *testing.T) {
	tests := []struct {
		actual, min corev1alpha1.AgentEventSeverity
		want        bool
	}{
		{corev1alpha1.EventSeverityCritical, corev1alpha1.EventSeverityCritical, true},
		{corev1alpha1.EventSeverityCritical, corev1alpha1.EventSeverityWarning, true},
		{corev1alpha1.EventSeverityCritical, corev1alpha1.EventSeverityInfo, true},
		{corev1alpha1.EventSeverityWarning, corev1alpha1.EventSeverityCritical, false},
		{corev1alpha1.EventSeverityInfo, corev1alpha1.EventSeverityWarning, false},
		{corev1alpha1.EventSeverityInfo, corev1alpha1.EventSeverityInfo, true},
	}
	for _, tt := range tests {
		got := severityMeets(tt.actual, tt.min)
		if got != tt.want {
			t.Errorf("severityMeets(%q, %q) = %v, want %v", tt.actual, tt.min, got, tt.want)
		}
	}
}
