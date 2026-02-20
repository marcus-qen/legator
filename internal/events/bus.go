/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package events implements the agent coordination event bus.
// Agents publish AgentEvent CRDs when they discover something noteworthy.
// Other agents can subscribe to events matching certain criteria and be
// triggered to run in response.
//
// The event bus is CRD-based for persistence and HA safety:
//   - Events survive controller restarts
//   - Multiple controller replicas see the same events
//   - Standard K8s RBAC controls access
//
// Event lifecycle: New → Delivered → Consumed → (TTL expiry → deleted)
package events

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
)

// Bus manages the agent event lifecycle.
type Bus struct {
	client client.Client
	log    logr.Logger
}

// NewBus creates a new event bus.
func NewBus(c client.Client, log logr.Logger) *Bus {
	return &Bus{
		client: c,
		log:    log,
	}
}

// Publish creates a new AgentEvent.
func (b *Bus) Publish(ctx context.Context, params PublishParams) (*corev1alpha1.AgentEvent, error) {
	event := &corev1alpha1.AgentEvent{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-event-", params.SourceAgent),
			Namespace:    params.Namespace,
			Labels: map[string]string{
				"legator.io/source-agent": params.SourceAgent,
				"legator.io/event-type":   params.EventType,
				"legator.io/severity":     string(params.Severity),
			},
		},
		Spec: corev1alpha1.AgentEventSpec{
			SourceAgent: params.SourceAgent,
			EventType:   params.EventType,
			Severity:    params.Severity,
			Summary:     params.Summary,
			Detail:      params.Detail,
			TargetAgent: params.TargetAgent,
			Labels:      params.Labels,
			TTL:         params.TTL,
		},
	}

	if err := b.client.Create(ctx, event); err != nil {
		return nil, fmt.Errorf("create AgentEvent: %w", err)
	}

	// Set status after create (status subresource requires separate update)
	event.Status.Phase = corev1alpha1.EventPhaseNew
	if err := b.client.Status().Update(ctx, event); err != nil {
		b.log.Error(err, "failed to set initial event status", "name", event.Name)
	}

	b.log.Info("AgentEvent published",
		"name", event.Name,
		"source", params.SourceAgent,
		"type", params.EventType,
		"severity", params.Severity,
		"summary", params.Summary,
	)

	return event, nil
}

// Consume marks an event as consumed by an agent and optionally records the triggered run.
func (b *Bus) Consume(ctx context.Context, eventName, namespace, agentName, runName string) error {
	event := &corev1alpha1.AgentEvent{}
	if err := b.client.Get(ctx, client.ObjectKey{Name: eventName, Namespace: namespace}, event); err != nil {
		return fmt.Errorf("get AgentEvent: %w", err)
	}

	// Add consumer
	event.Status.ConsumedBy = append(event.Status.ConsumedBy, corev1alpha1.EventConsumer{
		Agent: agentName,
		ConsumedAt: metav1.Now(),
	})

	// Track triggered run
	if runName != "" {
		event.Status.TriggeredRuns = append(event.Status.TriggeredRuns, runName)
	}

	// Update phase
	event.Status.Phase = corev1alpha1.EventPhaseConsumed

	if err := b.client.Status().Update(ctx, event); err != nil {
		return fmt.Errorf("update AgentEvent status: %w", err)
	}

	return nil
}

// FindNewEvents returns events matching the given criteria that haven't been
// consumed by the specified agent yet.
func (b *Bus) FindNewEvents(ctx context.Context, params SubscribeParams) ([]corev1alpha1.AgentEvent, error) {
	list := &corev1alpha1.AgentEventList{}
	opts := []client.ListOption{
		client.InNamespace(params.Namespace),
	}

	if err := b.client.List(ctx, list, opts...); err != nil {
		return nil, fmt.Errorf("list AgentEvents: %w", err)
	}

	var matched []corev1alpha1.AgentEvent
	for _, event := range list.Items {
		// Skip expired events
		if event.Status.Phase == corev1alpha1.EventPhaseExpired {
			continue
		}

		// Filter by event type
		if params.EventType != "" && event.Spec.EventType != params.EventType {
			continue
		}

		// Filter by source agent
		if params.SourceAgent != "" && event.Spec.SourceAgent != params.SourceAgent {
			continue
		}

		// Skip if targeted to a different agent
		if event.Spec.TargetAgent != "" && event.Spec.TargetAgent != params.ConsumerAgent {
			continue
		}

		// Check severity filter
		if params.MinSeverity != "" && !severityMeets(event.Spec.Severity, params.MinSeverity) {
			continue
		}

		// Skip if already consumed by this agent
		alreadyConsumed := false
		for _, c := range event.Status.ConsumedBy {
			if c.Agent == params.ConsumerAgent {
				alreadyConsumed = true
				break
			}
		}
		if alreadyConsumed {
			continue
		}

		// Check TTL (only if creation timestamp is set — fake clients may not set it)
		if event.Spec.TTL != "" && !event.CreationTimestamp.IsZero() {
			ttl, err := time.ParseDuration(event.Spec.TTL)
			if err == nil && time.Since(event.CreationTimestamp.Time) > ttl {
				continue // expired
			}
		}

		matched = append(matched, event)
	}

	return matched, nil
}

// CleanExpired deletes AgentEvents that have exceeded their TTL.
func (b *Bus) CleanExpired(ctx context.Context, namespace string, defaultTTL time.Duration) (int, error) {
	list := &corev1alpha1.AgentEventList{}
	if err := b.client.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return 0, err
	}

	deleted := 0
	for _, event := range list.Items {
		ttl := defaultTTL
		if event.Spec.TTL != "" {
			if d, err := time.ParseDuration(event.Spec.TTL); err == nil {
				ttl = d
			}
		}

		if time.Since(event.CreationTimestamp.Time) > ttl {
			if err := b.client.Delete(ctx, &event); err != nil {
				b.log.Error(err, "failed to delete expired event", "name", event.Name)
				continue
			}
			deleted++
		}
	}

	return deleted, nil
}

// --- Types ---

// PublishParams holds the parameters for publishing an event.
type PublishParams struct {
	SourceAgent string
	Namespace   string
	EventType   string
	Severity    corev1alpha1.AgentEventSeverity
	Summary     string
	Detail      string
	TargetAgent string
	Labels      map[string]string
	TTL         string
}

// SubscribeParams holds the criteria for finding events.
type SubscribeParams struct {
	Namespace     string
	ConsumerAgent string
	EventType     string
	SourceAgent   string
	MinSeverity   corev1alpha1.AgentEventSeverity
}

// severityMeets returns true if the event severity meets or exceeds the minimum.
func severityMeets(actual, minimum corev1alpha1.AgentEventSeverity) bool {
	order := map[corev1alpha1.AgentEventSeverity]int{
		corev1alpha1.EventSeverityInfo:     0,
		corev1alpha1.EventSeverityWarning:  1,
		corev1alpha1.EventSeverityCritical: 2,
	}
	return order[actual] >= order[minimum]
}
