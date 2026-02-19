/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package metrics defines Prometheus metrics for the InfraAgent operator.
//
// All metrics are registered with the controller-runtime default registry
// so they are automatically served on the metrics endpoint.
//
// Metric naming follows Prometheus conventions:
//   - infraagent_ prefix for all custom metrics
//   - _total suffix for counters
//   - _seconds suffix for duration histograms
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// RunsTotal counts agent runs by agent name and terminal status.
	RunsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "infraagent_runs_total",
			Help: "Total number of agent runs by agent and status.",
		},
		[]string{"agent", "status"},
	)

	// RunDurationSeconds is a histogram of run duration by agent.
	RunDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "infraagent_run_duration_seconds",
			Help:    "Duration of agent runs in seconds.",
			Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600, 1200, 2400},
		},
		[]string{"agent"},
	)

	// TokensUsedTotal counts tokens consumed by agent and model.
	TokensUsedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "infraagent_tokens_used_total",
			Help: "Total tokens consumed by agent runs.",
		},
		[]string{"agent", "model"},
	)

	// IterationsTotal counts tool-call loop iterations by agent.
	IterationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "infraagent_iterations_total",
			Help: "Total tool-call loop iterations across all runs.",
		},
		[]string{"agent"},
	)

	// GuardrailBlocksTotal counts blocked actions by agent and action tool name.
	GuardrailBlocksTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "infraagent_guardrail_blocks_total",
			Help: "Total actions blocked by guardrails.",
		},
		[]string{"agent", "action"},
	)

	// FindingsTotal counts agent findings by agent and severity.
	FindingsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "infraagent_findings_total",
			Help: "Total findings reported by agents.",
		},
		[]string{"agent", "severity"},
	)

	// EscalationsTotal counts escalations by agent and reason.
	EscalationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "infraagent_escalations_total",
			Help: "Total escalations triggered by agents.",
		},
		[]string{"agent", "reason"},
	)

	// ScheduleLagSeconds is the delay between scheduled time and actual start.
	ScheduleLagSeconds = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "infraagent_schedule_lag_seconds",
			Help: "Seconds between scheduled run time and actual trigger.",
		},
		[]string{"agent"},
	)

	// ActiveRuns is the number of currently executing agent runs.
	ActiveRuns = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "infraagent_active_runs",
			Help: "Number of agent runs currently executing.",
		},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		RunsTotal,
		RunDurationSeconds,
		TokensUsedTotal,
		IterationsTotal,
		GuardrailBlocksTotal,
		FindingsTotal,
		EscalationsTotal,
		ScheduleLagSeconds,
		ActiveRuns,
	)
}

// RecordRunComplete records metrics for a completed agent run.
func RecordRunComplete(agent, status, model string, duration time.Duration, tokensIn, tokensOut int64, iterations int32) {
	RunsTotal.WithLabelValues(agent, status).Inc()
	RunDurationSeconds.WithLabelValues(agent).Observe(duration.Seconds())
	TokensUsedTotal.WithLabelValues(agent, model).Add(float64(tokensIn + tokensOut))
	IterationsTotal.WithLabelValues(agent).Add(float64(iterations))
}

// RecordGuardrailBlock records a single blocked action.
func RecordGuardrailBlock(agent, action string) {
	GuardrailBlocksTotal.WithLabelValues(agent, action).Inc()
}

// RecordFinding records a single finding.
func RecordFinding(agent, severity string) {
	FindingsTotal.WithLabelValues(agent, severity).Inc()
}

// RecordEscalation records a single escalation event.
func RecordEscalation(agent, reason string) {
	EscalationsTotal.WithLabelValues(agent, reason).Inc()
}

// RecordScheduleLag records the scheduling delay for an agent.
func RecordScheduleLag(agent string, lag time.Duration) {
	ScheduleLagSeconds.WithLabelValues(agent).Set(lag.Seconds())
}
