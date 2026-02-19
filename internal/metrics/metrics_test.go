/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func getCounterValue(cv *prometheus.CounterVec, labels ...string) float64 {
	m := &dto.Metric{}
	if err := cv.WithLabelValues(labels...).Write(m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}

func getGaugeValue(g prometheus.Gauge) float64 {
	m := &dto.Metric{}
	if err := g.Write(m); err != nil {
		return 0
	}
	return m.GetGauge().GetValue()
}

func getGaugeVecValue(gv *prometheus.GaugeVec, labels ...string) float64 {
	m := &dto.Metric{}
	if err := gv.WithLabelValues(labels...).Write(m); err != nil {
		return 0
	}
	return m.GetGauge().GetValue()
}

func getHistogramCount(hv *prometheus.HistogramVec, labels ...string) uint64 {
	m := &dto.Metric{}
	observer := hv.WithLabelValues(labels...)
	// Prometheus histogram implements prometheus.Metric via the observer
	if c, ok := observer.(prometheus.Metric); ok {
		if err := c.Write(m); err != nil {
			return 0
		}
		return m.GetHistogram().GetSampleCount()
	}
	return 0
}

func TestRecordRunComplete(t *testing.T) {
	// Record a run
	RecordRunComplete("test-agent", "Succeeded", "anthropic/claude-sonnet", 42*time.Second, 1000, 500, 3)

	// Verify counter incremented
	val := getCounterValue(RunsTotal, "test-agent", "Succeeded")
	if val < 1 {
		t.Errorf("RunsTotal = %f, want >= 1", val)
	}

	// Verify tokens counted
	tokens := getCounterValue(TokensUsedTotal, "test-agent", "anthropic/claude-sonnet")
	if tokens < 1500 {
		t.Errorf("TokensUsedTotal = %f, want >= 1500", tokens)
	}

	// Verify iterations counted
	iters := getCounterValue(IterationsTotal, "test-agent")
	if iters < 3 {
		t.Errorf("IterationsTotal = %f, want >= 3", iters)
	}

	// Verify histogram has an observation
	count := getHistogramCount(RunDurationSeconds, "test-agent")
	if count < 1 {
		t.Errorf("RunDurationSeconds sample count = %d, want >= 1", count)
	}
}

func TestRecordGuardrailBlock(t *testing.T) {
	RecordGuardrailBlock("watchman", "kubectl.delete")
	RecordGuardrailBlock("watchman", "kubectl.delete")

	val := getCounterValue(GuardrailBlocksTotal, "watchman", "kubectl.delete")
	if val < 2 {
		t.Errorf("GuardrailBlocksTotal = %f, want >= 2", val)
	}
}

func TestRecordFinding(t *testing.T) {
	RecordFinding("vigil", "critical")

	val := getCounterValue(FindingsTotal, "vigil", "critical")
	if val < 1 {
		t.Errorf("FindingsTotal = %f, want >= 1", val)
	}
}

func TestRecordEscalation(t *testing.T) {
	RecordEscalation("forge", "autonomy ceiling")

	val := getCounterValue(EscalationsTotal, "forge", "autonomy ceiling")
	if val < 1 {
		t.Errorf("EscalationsTotal = %f, want >= 1", val)
	}
}

func TestRecordScheduleLag(t *testing.T) {
	RecordScheduleLag("watchman-light", 12*time.Second)

	val := getGaugeVecValue(ScheduleLagSeconds, "watchman-light")
	if val != 12 {
		t.Errorf("ScheduleLagSeconds = %f, want 12", val)
	}

	// Update it
	RecordScheduleLag("watchman-light", 3*time.Second)
	val = getGaugeVecValue(ScheduleLagSeconds, "watchman-light")
	if val != 3 {
		t.Errorf("ScheduleLagSeconds after update = %f, want 3", val)
	}
}

func TestActiveRuns(t *testing.T) {
	ActiveRuns.Set(0) // Reset

	ActiveRuns.Inc()
	ActiveRuns.Inc()

	val := getGaugeValue(ActiveRuns)
	if val != 2 {
		t.Errorf("ActiveRuns = %f, want 2", val)
	}

	ActiveRuns.Dec()
	val = getGaugeValue(ActiveRuns)
	if val != 1 {
		t.Errorf("ActiveRuns after Dec = %f, want 1", val)
	}
}

func TestMultipleAgentsMetrics(t *testing.T) {
	// Verify label isolation
	RecordRunComplete("agent-a", "Succeeded", "openai/gpt-4", 10*time.Second, 100, 50, 1)
	RecordRunComplete("agent-b", "Failed", "anthropic/claude", 5*time.Second, 200, 100, 2)

	aSucceeded := getCounterValue(RunsTotal, "agent-a", "Succeeded")
	bFailed := getCounterValue(RunsTotal, "agent-b", "Failed")
	aFailed := getCounterValue(RunsTotal, "agent-a", "Failed")

	if aSucceeded < 1 {
		t.Error("agent-a Succeeded should be >= 1")
	}
	if bFailed < 1 {
		t.Error("agent-b Failed should be >= 1")
	}
	if aFailed != 0 {
		t.Errorf("agent-a Failed = %f, want 0", aFailed)
	}
}
