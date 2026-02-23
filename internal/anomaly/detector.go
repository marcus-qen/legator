/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package anomaly

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Config configures baseline anomaly detection heuristics.
type Config struct {
	Namespace string

	ScanInterval time.Duration
	Lookback     time.Duration

	FrequencyWindow    time.Duration
	FrequencyThreshold int

	ScopeSpikeMultiplier float64
	MinScopeSpikeDelta   int

	TargetDriftMinSamples int
}

// DefaultConfig returns sensible baseline defaults for v0.9.0 heuristics.
func DefaultConfig() Config {
	return Config{
		Namespace:             "agents",
		ScanInterval:          2 * time.Minute,
		Lookback:              24 * time.Hour,
		FrequencyWindow:       30 * time.Minute,
		FrequencyThreshold:    6,
		ScopeSpikeMultiplier:  2.5,
		MinScopeSpikeDelta:    5,
		TargetDriftMinSamples: 5,
	}
}

// Detector periodically analyzes run patterns and emits anomaly events.
type Detector struct {
	client client.Client
	cfg    Config
	log    logr.Logger
}

// NewDetector creates a new anomaly detector runnable.
func NewDetector(c client.Client, cfg Config, log logr.Logger) *Detector {
	defaults := DefaultConfig()
	if cfg.Namespace == "" {
		cfg.Namespace = defaults.Namespace
	}
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = defaults.ScanInterval
	}
	if cfg.Lookback <= 0 {
		cfg.Lookback = defaults.Lookback
	}
	if cfg.FrequencyWindow <= 0 {
		cfg.FrequencyWindow = defaults.FrequencyWindow
	}
	if cfg.FrequencyThreshold <= 0 {
		cfg.FrequencyThreshold = defaults.FrequencyThreshold
	}
	if cfg.ScopeSpikeMultiplier <= 0 {
		cfg.ScopeSpikeMultiplier = defaults.ScopeSpikeMultiplier
	}
	if cfg.MinScopeSpikeDelta <= 0 {
		cfg.MinScopeSpikeDelta = defaults.MinScopeSpikeDelta
	}
	if cfg.TargetDriftMinSamples <= 0 {
		cfg.TargetDriftMinSamples = defaults.TargetDriftMinSamples
	}

	return &Detector{
		client: c,
		cfg:    cfg,
		log:    log.WithName("anomaly-detector"),
	}
}

// Start runs the periodic anomaly detection loop.
func (d *Detector) Start(ctx context.Context) error {
	d.log.Info("Anomaly detector starting",
		"namespace", d.cfg.Namespace,
		"interval", d.cfg.ScanInterval.String(),
	)

	if err := d.ScanOnce(ctx); err != nil {
		d.log.Error(err, "Initial anomaly scan failed")
	}

	ticker := time.NewTicker(d.cfg.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.log.Info("Anomaly detector stopping")
			return nil
		case <-ticker.C:
			if err := d.ScanOnce(ctx); err != nil {
				d.log.Error(err, "Anomaly scan failed")
			}
		}
	}
}

// NeedLeaderElection ensures only the elected manager performs anomaly scans.
func (d *Detector) NeedLeaderElection() bool {
	return true
}

// ScanOnce performs one anomaly scan cycle.
func (d *Detector) ScanOnce(ctx context.Context) error {
	runs := &corev1alpha1.LegatorRunList{}
	opts := []client.ListOption{}
	if d.cfg.Namespace != "" {
		opts = append(opts, client.InNamespace(d.cfg.Namespace))
	}
	if err := d.client.List(ctx, runs, opts...); err != nil {
		return fmt.Errorf("list runs: %w", err)
	}

	sort.Slice(runs.Items, func(i, j int) bool {
		return runs.Items[i].CreationTimestamp.Before(&runs.Items[j].CreationTimestamp)
	})

	historyByAgent := map[string][]runSnapshot{}
	emitted := 0

	for _, run := range runs.Items {
		if run.Spec.Trigger != corev1alpha1.RunTriggerManual {
			continue
		}
		if !isTerminal(run.Status.Phase) {
			continue
		}

		snapshot := summarizeRun(run)
		history := historyByAgent[snapshot.Agent]
		signals := detectAnomalies(snapshot, history, d.cfg)

		for _, signal := range signals {
			if err := d.recordAnomalyEvent(ctx, run, signal); err != nil {
				d.log.Error(err, "Failed to record anomaly event", "run", run.Name, "type", signal.Type)
				continue
			}
			emitted++
		}

		historyByAgent[snapshot.Agent] = append(historyByAgent[snapshot.Agent], snapshot)
	}

	if emitted > 0 {
		d.log.Info("Anomaly scan completed", "eventsEmitted", emitted)
	}

	return nil
}

type runSnapshot struct {
	Namespace     string
	RunName       string
	Agent         string
	Timestamp     time.Time
	ActionCount   int
	TargetClasses []string
}

type anomalySignal struct {
	Type     string
	Severity corev1alpha1.AgentEventSeverity
	Summary  string
	Detail   string
	Labels   map[string]string
}

func detectAnomalies(current runSnapshot, history []runSnapshot, cfg Config) []anomalySignal {
	relevant := filterLookback(history, current.Timestamp, cfg.Lookback)
	if len(relevant) == 0 {
		return nil
	}

	var out []anomalySignal

	if signal, ok := detectFrequencySpike(current, relevant, cfg); ok {
		out = append(out, signal)
	}
	if signal, ok := detectScopeSpike(current, relevant, cfg); ok {
		out = append(out, signal)
	}
	if signal, ok := detectTargetDrift(current, relevant, cfg); ok {
		out = append(out, signal)
	}

	return out
}

func detectFrequencySpike(current runSnapshot, history []runSnapshot, cfg Config) (anomalySignal, bool) {
	recent := 1 // include current
	for _, item := range history {
		if current.Timestamp.Sub(item.Timestamp) <= cfg.FrequencyWindow {
			recent++
		}
	}
	if recent <= cfg.FrequencyThreshold {
		return anomalySignal{}, false
	}

	severity := corev1alpha1.EventSeverityWarning
	if recent >= cfg.FrequencyThreshold*2 {
		severity = corev1alpha1.EventSeverityCritical
	}

	return anomalySignal{
		Type:     "frequency-spike",
		Severity: severity,
		Summary: fmt.Sprintf(
			"Run frequency anomaly for agent %s: %d manual runs within %s (threshold=%d)",
			current.Agent,
			recent,
			cfg.FrequencyWindow.Round(time.Second).String(),
			cfg.FrequencyThreshold,
		),
		Detail: fmt.Sprintf(
			"Detected %d manual runs for agent %s in the last %s; baseline threshold is %d.",
			recent,
			current.Agent,
			cfg.FrequencyWindow.Round(time.Second).String(),
			cfg.FrequencyThreshold,
		),
		Labels: map[string]string{
			"anomaly-kind": "frequency",
			"window":       cfg.FrequencyWindow.String(),
		},
	}, true
}

func detectScopeSpike(current runSnapshot, history []runSnapshot, cfg Config) (anomalySignal, bool) {
	if len(history) < 3 {
		return anomalySignal{}, false
	}

	var total int
	for _, item := range history {
		total += item.ActionCount
	}
	avg := float64(total) / float64(len(history))
	threshold := int(math.Ceil(avg * cfg.ScopeSpikeMultiplier))
	if current.ActionCount < threshold {
		return anomalySignal{}, false
	}
	if current.ActionCount-int(math.Round(avg)) < cfg.MinScopeSpikeDelta {
		return anomalySignal{}, false
	}

	return anomalySignal{
		Type:     "scope-spike",
		Severity: corev1alpha1.EventSeverityWarning,
		Summary: fmt.Sprintf(
			"Scope anomaly for agent %s: %d actions vs baseline %.1f (multiplier=%.2f)",
			current.Agent,
			current.ActionCount,
			avg,
			cfg.ScopeSpikeMultiplier,
		),
		Detail: fmt.Sprintf(
			"Current run action count %d exceeded spike threshold %d (avg %.1f * %.2f).",
			current.ActionCount,
			threshold,
			avg,
			cfg.ScopeSpikeMultiplier,
		),
		Labels: map[string]string{
			"anomaly-kind": "scope",
		},
	}, true
}

func detectTargetDrift(current runSnapshot, history []runSnapshot, cfg Config) (anomalySignal, bool) {
	if len(history) < cfg.TargetDriftMinSamples {
		return anomalySignal{}, false
	}
	if len(current.TargetClasses) == 0 {
		return anomalySignal{}, false
	}

	seen := map[string]struct{}{}
	for _, item := range history {
		for _, targetClass := range item.TargetClasses {
			seen[targetClass] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return anomalySignal{}, false
	}

	newTargets := make([]string, 0, len(current.TargetClasses))
	for _, targetClass := range current.TargetClasses {
		if _, ok := seen[targetClass]; !ok {
			newTargets = append(newTargets, targetClass)
		}
	}
	if len(newTargets) == 0 {
		return anomalySignal{}, false
	}

	sort.Strings(newTargets)
	if len(newTargets) > 5 {
		newTargets = newTargets[:5]
	}

	return anomalySignal{
		Type:     "target-drift",
		Severity: corev1alpha1.EventSeverityWarning,
		Summary: fmt.Sprintf(
			"Target drift anomaly for agent %s: new target classes %s",
			current.Agent,
			strings.Join(newTargets, ", "),
		),
		Detail: fmt.Sprintf(
			"Current run references unseen target classes (%s) compared with %d recent runs.",
			strings.Join(newTargets, ", "),
			len(history),
		),
		Labels: map[string]string{
			"anomaly-kind": "target-drift",
		},
	}, true
}

func filterLookback(history []runSnapshot, now time.Time, lookback time.Duration) []runSnapshot {
	if lookback <= 0 {
		return history
	}
	out := make([]runSnapshot, 0, len(history))
	for _, item := range history {
		if now.Sub(item.Timestamp) <= lookback {
			out = append(out, item)
		}
	}
	return out
}

func summarizeRun(run corev1alpha1.LegatorRun) runSnapshot {
	timestamp := run.CreationTimestamp.Time
	if run.Status.StartTime != nil {
		timestamp = run.Status.StartTime.Time
	}

	targetClasses := map[string]struct{}{}
	for _, action := range run.Status.Actions {
		targetClass := normalizeTargetClass(action.Target)
		if targetClass == "" {
			continue
		}
		targetClasses[targetClass] = struct{}{}
	}
	targets := make([]string, 0, len(targetClasses))
	for targetClass := range targetClasses {
		targets = append(targets, targetClass)
	}
	sort.Strings(targets)

	return runSnapshot{
		Namespace:     run.Namespace,
		RunName:       run.Name,
		Agent:         run.Spec.AgentRef,
		Timestamp:     timestamp,
		ActionCount:   len(run.Status.Actions),
		TargetClasses: targets,
	}
}

func normalizeTargetClass(target string) string {
	trimmed := strings.TrimSpace(strings.ToLower(target))
	if trimmed == "" {
		return ""
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return ""
	}

	first := fields[0]
	if strings.HasPrefix(first, "-") && len(fields) > 1 {
		first = fields[1]
	}
	if idx := strings.Index(first, "/"); idx > 0 {
		first = first[:idx]
	}
	if idx := strings.Index(first, ":"); idx > 0 {
		first = first[:idx]
	}

	return first
}

func isTerminal(phase corev1alpha1.RunPhase) bool {
	switch phase {
	case corev1alpha1.RunPhaseSucceeded,
		corev1alpha1.RunPhaseFailed,
		corev1alpha1.RunPhaseEscalated,
		corev1alpha1.RunPhaseBlocked:
		return true
	default:
		return false
	}
}

func (d *Detector) recordAnomalyEvent(ctx context.Context, run corev1alpha1.LegatorRun, signal anomalySignal) error {
	key := fmt.Sprintf("%s/%s/%s", run.Namespace, run.Name, signal.Type)

	existing := &corev1alpha1.AgentEventList{}
	if err := d.client.List(
		ctx,
		existing,
		client.InNamespace(run.Namespace),
		client.MatchingLabels{"legator.io/anomaly-key": key},
	); err != nil {
		return fmt.Errorf("list existing anomaly events: %w", err)
	}
	if len(existing.Items) > 0 {
		return nil
	}

	labels := map[string]string{
		"legator.io/source-agent": run.Spec.AgentRef,
		"legator.io/event-type":   "anomaly",
		"legator.io/severity":     string(signal.Severity),
		"legator.io/anomaly-type": signal.Type,
		"legator.io/anomaly-key":  key,
	}
	for k, v := range signal.Labels {
		labels[k] = v
	}

	event := &corev1alpha1.AgentEvent{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-anomaly-", run.Spec.AgentRef),
			Namespace:    run.Namespace,
			Labels:       labels,
		},
		Spec: corev1alpha1.AgentEventSpec{
			SourceAgent: run.Spec.AgentRef,
			SourceRun:   run.Name,
			EventType:   "anomaly",
			Severity:    signal.Severity,
			Summary:     signal.Summary,
			Detail:      signal.Detail,
			Labels:      signal.Labels,
			TTL:         "24h",
		},
	}

	if err := d.client.Create(ctx, event); err != nil {
		return fmt.Errorf("create anomaly event: %w", err)
	}

	return nil
}
