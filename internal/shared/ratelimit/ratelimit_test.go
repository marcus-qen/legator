/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ratelimit

import (
	"testing"
)

func TestAllow_UnderLimits(t *testing.T) {
	l := NewLimiter(DefaultConfig())
	d := l.Allow("ns/agent-a", false)
	if !d.Allowed {
		t.Fatalf("expected allowed, got: %s", d.Reason)
	}
}

func TestAllow_PerAgentConcurrency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxConcurrentPerAgent = 1
	l := NewLimiter(cfg)

	l.RecordStart("ns/agent-a")

	d := l.Allow("ns/agent-a", false)
	if d.Allowed {
		t.Fatal("expected blocked by per-agent concurrency")
	}

	// Different agent should still be allowed
	d2 := l.Allow("ns/agent-b", false)
	if !d2.Allowed {
		t.Fatalf("different agent should be allowed: %s", d2.Reason)
	}
}

func TestAllow_ClusterWideConcurrency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxConcurrentCluster = 2
	cfg.MaxConcurrentPerAgent = 5
	l := NewLimiter(cfg)

	l.RecordStart("ns/a")
	l.RecordStart("ns/b")

	d := l.Allow("ns/c", false)
	if d.Allowed {
		t.Fatal("expected blocked by cluster-wide concurrency")
	}

	// Webhook trigger gets burst allowance
	d2 := l.Allow("ns/c", true)
	if !d2.Allowed {
		t.Fatalf("webhook should get burst allowance: %s", d2.Reason)
	}
}

func TestAllow_PerAgentRate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxRunsPerHourPerAgent = 3
	cfg.MaxConcurrentPerAgent = 100
	cfg.MaxConcurrentCluster = 100
	l := NewLimiter(cfg)

	// Record 3 runs (start + complete to avoid concurrency block)
	for i := 0; i < 3; i++ {
		l.RecordStart("ns/agent-x")
		l.RecordComplete("ns/agent-x")
	}

	d := l.Allow("ns/agent-x", false)
	if d.Allowed {
		t.Fatal("expected blocked by per-agent rate limit")
	}

	// Different agent should be fine
	d2 := l.Allow("ns/agent-y", false)
	if !d2.Allowed {
		t.Fatalf("different agent should be allowed: %s", d2.Reason)
	}
}

func TestAllow_ClusterWideRate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxRunsPerHourCluster = 5
	cfg.MaxRunsPerHourPerAgent = 100
	cfg.MaxConcurrentPerAgent = 100
	cfg.MaxConcurrentCluster = 100
	l := NewLimiter(cfg)

	for i := 0; i < 5; i++ {
		l.RecordStart("ns/agent-" + string(rune('a'+i)))
		l.RecordComplete("ns/agent-" + string(rune('a'+i)))
	}

	d := l.Allow("ns/agent-z", false)
	if d.Allowed {
		t.Fatal("expected blocked by cluster-wide rate limit")
	}
}

func TestRecordStartComplete(t *testing.T) {
	l := NewLimiter(DefaultConfig())

	l.RecordStart("ns/a")
	l.RecordStart("ns/a")
	stats := l.GetStats()
	if stats.ConcurrentTotal != 2 {
		t.Fatalf("expected 2 concurrent, got %d", stats.ConcurrentTotal)
	}
	if stats.ConcurrentByAgent["ns/a"] != 2 {
		t.Fatalf("expected 2 for agent-a, got %d", stats.ConcurrentByAgent["ns/a"])
	}

	l.RecordComplete("ns/a")
	stats = l.GetStats()
	if stats.ConcurrentTotal != 1 {
		t.Fatalf("expected 1 concurrent, got %d", stats.ConcurrentTotal)
	}

	l.RecordComplete("ns/a")
	stats = l.GetStats()
	if stats.ConcurrentTotal != 0 {
		t.Fatalf("expected 0 concurrent, got %d", stats.ConcurrentTotal)
	}

	// Complete on empty should not go negative
	l.RecordComplete("ns/a")
	stats = l.GetStats()
	if stats.ConcurrentTotal != 0 {
		t.Fatalf("should not go negative, got %d", stats.ConcurrentTotal)
	}
}

func TestGetStats(t *testing.T) {
	l := NewLimiter(DefaultConfig())

	l.RecordStart("ns/a")
	l.RecordStart("ns/b")
	l.RecordStart("ns/b")

	stats := l.GetStats()
	if stats.ConcurrentTotal != 3 {
		t.Fatalf("expected 3, got %d", stats.ConcurrentTotal)
	}
	if stats.ConcurrentByAgent["ns/a"] != 1 {
		t.Fatalf("expected 1 for a, got %d", stats.ConcurrentByAgent["ns/a"])
	}
	if stats.ConcurrentByAgent["ns/b"] != 2 {
		t.Fatalf("expected 2 for b, got %d", stats.ConcurrentByAgent["ns/b"])
	}
	if stats.RunsLastHour != 3 {
		t.Fatalf("expected 3 runs in history, got %d", stats.RunsLastHour)
	}
}
