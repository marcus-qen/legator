/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/marcus-qen/infraagent/api/v1alpha1"
	"github.com/marcus-qen/infraagent/internal/metrics"
	"github.com/marcus-qen/infraagent/internal/runner"
)

// Scheduler manages the lifecycle of scheduled agent runs.
// It periodically checks all InfraAgent CRs, triggers due agents,
// respects concurrency limits, and updates schedule status.
//
// The Scheduler runs as a Runnable in the controller-runtime manager,
// which provides leader election for free.
type Scheduler struct {
	client  client.Client
	runner  *runner.Runner
	tracker *RunTracker
	webhook *WebhookHandler
	log     logr.Logger

	// checkInterval is how often the scheduler scans for due agents.
	// Default: 10 seconds.
	checkInterval time.Duration

	// maxConcurrentRuns is the cluster-wide limit on simultaneous runs.
	// Default: 10.
	maxConcurrentRuns int

	// jitterPercent is the jitter applied to scheduled times.
	// Default: 10%.
	jitterPercent float64

	// runConfigFactory builds RunConfig for an agent.
	// Must be set before Start().
	RunConfigFactory func(agent *corev1alpha1.InfraAgent) (runner.RunConfig, error)
}

// Config configures the scheduler.
type Config struct {
	CheckInterval     time.Duration
	MaxConcurrentRuns int
	JitterPercent     float64
	WebhookDebounce   time.Duration
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		CheckInterval:     10 * time.Second,
		MaxConcurrentRuns: 10,
		JitterPercent:     10.0,
		WebhookDebounce:   30 * time.Second,
	}
}

// New creates a new Scheduler.
func New(c client.Client, r *runner.Runner, log logr.Logger, cfg Config) *Scheduler {
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = 10 * time.Second
	}
	if cfg.MaxConcurrentRuns <= 0 {
		cfg.MaxConcurrentRuns = 10
	}
	if cfg.JitterPercent <= 0 {
		cfg.JitterPercent = 10.0
	}

	return &Scheduler{
		client:            c,
		runner:            r,
		tracker:           NewRunTracker(),
		webhook:           NewWebhookHandler(log.WithName("webhook"), cfg.WebhookDebounce),
		log:               log.WithName("scheduler"),
		checkInterval:     cfg.CheckInterval,
		maxConcurrentRuns: cfg.MaxConcurrentRuns,
		jitterPercent:     cfg.JitterPercent,
	}
}

// WebhookHandler returns the HTTP handler for webhook triggers.
func (s *Scheduler) WebhookHandler() *WebhookHandler {
	return s.webhook
}

// RunTracker returns the concurrency tracker (for status reporting).
func (s *Scheduler) RunTrackerRef() *RunTracker {
	return s.tracker
}

// Start implements manager.Runnable. Called by controller-runtime when the
// manager starts (after leader election if HA).
func (s *Scheduler) Start(ctx context.Context) error {
	s.log.Info("Scheduler starting",
		"checkInterval", s.checkInterval,
		"maxConcurrentRuns", s.maxConcurrentRuns,
		"jitterPercent", s.jitterPercent,
	)

	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()

	// Also drain webhook triggers
	for {
		select {
		case <-ctx.Done():
			s.log.Info("Scheduler stopping")
			return nil

		case <-ticker.C:
			s.tick(ctx)

		case trigger := <-s.webhook.Triggers():
			s.handleWebhookTrigger(ctx, trigger)
		}
	}
}

// tick runs one scheduling cycle: list all agents, trigger any that are due.
func (s *Scheduler) tick(ctx context.Context) {
	// Clean stale in-flight tracking (runs that crashed without completing)
	if cleaned := s.tracker.CleanStale(30 * time.Minute); cleaned > 0 {
		s.log.Info("Cleaned stale in-flight runs", "count", cleaned)
	}

	// List all InfraAgents
	agentList := &corev1alpha1.InfraAgentList{}
	if err := s.client.List(ctx, agentList); err != nil {
		s.log.Error(err, "Failed to list InfraAgents")
		return
	}

	now := time.Now()
	for i := range agentList.Items {
		agent := &agentList.Items[i]
		s.evaluateAgent(ctx, agent, now)
	}
}

// evaluateAgent checks if a single agent is due and triggers if so.
func (s *Scheduler) evaluateAgent(ctx context.Context, agent *corev1alpha1.InfraAgent, now time.Time) {
	// Step 3.6: Pause/resume
	if agent.Spec.Paused {
		return
	}

	// Only evaluate scheduled agents (cron or interval)
	if agent.Spec.Schedule.Cron == "" && agent.Spec.Schedule.Interval == "" {
		return
	}

	agentKey := fmt.Sprintf("%s/%s", agent.Namespace, agent.Name)

	// Step 3.5: Concurrency — one run at a time per agent
	if s.tracker.IsRunning(agentKey) {
		return
	}

	// Cluster-wide concurrency limit
	if s.tracker.InFlightCount() >= s.maxConcurrentRuns {
		s.log.V(1).Info("Max concurrent runs reached, skipping",
			"agent", agent.Name,
			"inflight", s.tracker.InFlightCount(),
		)
		return
	}

	// Check if due
	due, err := IsDue(agent, now)
	if err != nil {
		s.log.Error(err, "Failed to check schedule", "agent", agent.Name)
		return
	}

	if !due {
		// Step 3.9: Update next run time on status
		s.updateNextRunTime(ctx, agent, now)
		return
	}

	// Metrics: record schedule lag (how late the trigger is)
	if agent.Status.NextRunTime != nil {
		lag := now.Sub(agent.Status.NextRunTime.Time)
		if lag > 0 {
			metrics.RecordScheduleLag(agent.Name, lag)
		}
	}

	// Trigger the run
	s.triggerRun(ctx, agent, agentKey, corev1alpha1.RunTriggerScheduled)
}

// triggerRun starts an agent run in a goroutine with concurrency tracking.
func (s *Scheduler) triggerRun(
	ctx context.Context,
	agent *corev1alpha1.InfraAgent,
	agentKey string,
	trigger corev1alpha1.RunTrigger,
) {
	runName := fmt.Sprintf("%s-run", agent.Name)

	if !s.tracker.TryStart(agentKey, runName) {
		s.log.Info("Agent already running, skipping", "agent", agent.Name)
		return
	}

	s.log.Info("Triggering agent run",
		"agent", agent.Name,
		"trigger", trigger,
		"inflight", s.tracker.InFlightCount(),
	)

	// Build run config
	var cfg runner.RunConfig
	if s.RunConfigFactory != nil {
		var err error
		cfg, err = s.RunConfigFactory(agent)
		if err != nil {
			s.log.Error(err, "Failed to create run config", "agent", agent.Name)
			s.tracker.Complete(agentKey)
			return
		}
	}
	cfg.Trigger = trigger

	// Run in goroutine (non-blocking)
	go func() {
		defer s.tracker.Complete(agentKey)

		runCtx := context.Background() // Independent of scheduler tick context
		agentRun, err := s.runner.Execute(runCtx, agent, cfg)
		if err != nil {
			s.log.Error(err, "Agent run failed",
				"agent", agent.Name,
				"trigger", trigger,
			)
		} else {
			s.log.Info("Agent run completed",
				"agent", agent.Name,
				"run", agentRun.Name,
				"phase", agentRun.Status.Phase,
			)
		}

		// Update agent status after run
		s.updateAgentAfterRun(context.Background(), agent, agentRun)
	}()
}

// handleWebhookTrigger processes a webhook-initiated trigger.
func (s *Scheduler) handleWebhookTrigger(ctx context.Context, trigger WebhookTrigger) {
	agentKey := fmt.Sprintf("%s/%s", trigger.AgentKey.Namespace, trigger.AgentKey.Name)

	// Fetch fresh agent state
	agent := &corev1alpha1.InfraAgent{}
	if err := s.client.Get(ctx, trigger.AgentKey, agent); err != nil {
		s.log.Error(err, "Failed to get agent for webhook trigger",
			"agent", trigger.AgentKey.String(),
			"source", trigger.Source,
		)
		return
	}

	// Respect pause
	if agent.Spec.Paused {
		s.log.Info("Webhook trigger ignored — agent paused",
			"agent", agent.Name,
			"source", trigger.Source,
		)
		return
	}

	s.triggerRun(ctx, agent, agentKey, corev1alpha1.RunTriggerWebhook)
}

// updateNextRunTime computes and updates the next scheduled run time on status.
func (s *Scheduler) updateNextRunTime(ctx context.Context, agent *corev1alpha1.InfraAgent, now time.Time) {
	next, err := NextRun(agent, now)
	if err != nil || next.IsZero() {
		return
	}

	// Apply jitter
	interval := ComputeInterval(agent)
	jittered := ApplyJitter(next, interval, s.jitterPercent)

	// Only update if changed (avoid unnecessary writes)
	if agent.Status.NextRunTime != nil && agent.Status.NextRunTime.Time.Equal(jittered) {
		return
	}

	agent.Status.NextRunTime = &metav1.Time{Time: jittered}

	// Set schedule condition
	meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type:               "Scheduled",
		Status:             metav1.ConditionTrue,
		Reason:             "ScheduleActive",
		Message:            fmt.Sprintf("Next run at %s", jittered.Format(time.RFC3339)),
		ObservedGeneration: agent.Generation,
	})

	if err := s.client.Status().Update(ctx, agent); err != nil {
		s.log.V(1).Info("Failed to update nextRunTime", "agent", agent.Name, "error", err)
	}
}

// updateAgentAfterRun updates the InfraAgent status after a run completes.
func (s *Scheduler) updateAgentAfterRun(ctx context.Context, agent *corev1alpha1.InfraAgent, agentRun *corev1alpha1.AgentRun) {
	// Refetch to avoid conflicts
	fresh := &corev1alpha1.InfraAgent{}
	key := types.NamespacedName{Namespace: agent.Namespace, Name: agent.Name}
	if err := s.client.Get(ctx, key, fresh); err != nil {
		s.log.Error(err, "Failed to refetch agent after run", "agent", agent.Name)
		return
	}

	now := metav1.Now()
	fresh.Status.LastRunTime = &now
	fresh.Status.RunCount++

	if agentRun != nil {
		fresh.Status.LastRunName = agentRun.Name
		if agentRun.Status.Phase == corev1alpha1.RunPhaseSucceeded {
			fresh.Status.ConsecutiveFailures = 0
		} else {
			fresh.Status.ConsecutiveFailures++
		}
	}

	// Compute next run time
	next, err := NextRun(fresh, now.Time)
	if err == nil && !next.IsZero() {
		interval := ComputeInterval(fresh)
		jittered := ApplyJitter(next, interval, s.jitterPercent)
		fresh.Status.NextRunTime = &metav1.Time{Time: jittered}
	}

	if err := s.client.Status().Update(ctx, fresh); err != nil {
		s.log.Error(err, "Failed to update agent status after run", "agent", agent.Name)
	}
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
// The scheduler should only run on the leader to prevent duplicate runs.
func (s *Scheduler) NeedLeaderElection() bool {
	return true
}

// RegisterWebhookTriggers scans an agent's trigger list and registers
// webhook sources with the webhook handler.
func (s *Scheduler) RegisterWebhookTriggers(agent *corev1alpha1.InfraAgent) {
	agentKey := types.NamespacedName{Namespace: agent.Namespace, Name: agent.Name}

	// Clear existing registrations
	s.webhook.UnregisterAgent(agentKey)

	// Register new ones
	for _, trigger := range agent.Spec.Schedule.Triggers {
		if trigger.Type == corev1alpha1.TriggerWebhook && trigger.Source != "" {
			s.webhook.RegisterAgent(trigger.Source, agentKey)
			s.log.Info("Registered webhook trigger",
				"agent", agent.Name,
				"source", trigger.Source,
			)
		}
	}
}
