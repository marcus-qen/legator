/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package ratelimit provides configurable rate limiting for agent runs.
// It enforces both cluster-wide and per-agent concurrency limits with
// configurable burst and sustained rates.
//
// This builds on the basic RunTracker in the scheduler package by adding:
//   - Per-agent rate limits (runs/hour)
//   - Cluster-wide rate limits (total runs/hour)
//   - Burst allowance for webhook-triggered runs
package ratelimit

import (
	"fmt"
	"sync"
	"time"
)

// Config configures rate limiting.
type Config struct {
	// MaxConcurrentCluster is the cluster-wide limit on simultaneous runs.
	MaxConcurrentCluster int

	// MaxConcurrentPerAgent is the per-agent limit on simultaneous runs.
	MaxConcurrentPerAgent int

	// MaxRunsPerHourCluster is the cluster-wide limit on total runs per hour.
	MaxRunsPerHourCluster int

	// MaxRunsPerHourPerAgent is the per-agent limit on runs per hour.
	MaxRunsPerHourPerAgent int

	// BurstAllowance allows this many extra runs for webhook triggers.
	BurstAllowance int
}

// DefaultConfig returns production defaults.
func DefaultConfig() Config {
	return Config{
		MaxConcurrentCluster:   10,
		MaxConcurrentPerAgent:  1,
		MaxRunsPerHourCluster:  200,
		MaxRunsPerHourPerAgent: 30,
		BurstAllowance:         3,
	}
}

// Decision represents whether a run is allowed and why.
type Decision struct {
	Allowed bool
	Reason  string
}

// Limiter tracks run concurrency and rates.
type Limiter struct {
	config Config

	mu sync.Mutex

	// concurrent tracks currently running agents
	concurrent map[string]int // agentKey → count
	totalConc  int

	// history tracks completed runs for rate calculation
	history []runRecord
}

type runRecord struct {
	agentKey string
	time     time.Time
}

// NewLimiter creates a rate limiter.
func NewLimiter(cfg Config) *Limiter {
	return &Limiter{
		config:     cfg,
		concurrent: make(map[string]int),
	}
}

// Allow checks whether a new run for the given agent is permitted.
func (l *Limiter) Allow(agentKey string, isWebhook bool) Decision {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.pruneHistory(now)

	// Per-agent concurrency
	if l.concurrent[agentKey] >= l.config.MaxConcurrentPerAgent {
		return Decision{
			Allowed: false,
			Reason:  fmt.Sprintf("per-agent concurrency limit reached (%d/%d)", l.concurrent[agentKey], l.config.MaxConcurrentPerAgent),
		}
	}

	// Cluster-wide concurrency
	maxConc := l.config.MaxConcurrentCluster
	if isWebhook {
		maxConc += l.config.BurstAllowance
	}
	if l.totalConc >= maxConc {
		return Decision{
			Allowed: false,
			Reason:  fmt.Sprintf("cluster-wide concurrency limit reached (%d/%d)", l.totalConc, maxConc),
		}
	}

	// Per-agent rate (runs/hour)
	agentCount := l.countAgent(agentKey, now)
	if agentCount >= l.config.MaxRunsPerHourPerAgent {
		return Decision{
			Allowed: false,
			Reason:  fmt.Sprintf("per-agent rate limit reached (%d runs in last hour, max %d)", agentCount, l.config.MaxRunsPerHourPerAgent),
		}
	}

	// Cluster-wide rate
	totalCount := len(l.history)
	maxRate := l.config.MaxRunsPerHourCluster
	if isWebhook {
		maxRate += l.config.BurstAllowance * 10
	}
	if totalCount >= maxRate {
		return Decision{
			Allowed: false,
			Reason:  fmt.Sprintf("cluster-wide rate limit reached (%d runs in last hour, max %d)", totalCount, maxRate),
		}
	}

	return Decision{Allowed: true}
}

// RecordStart marks a run as started.
func (l *Limiter) RecordStart(agentKey string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.concurrent[agentKey]++
	l.totalConc++
	l.history = append(l.history, runRecord{agentKey: agentKey, time: time.Now()})
}

// RecordComplete marks a run as finished.
func (l *Limiter) RecordComplete(agentKey string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.concurrent[agentKey] > 0 {
		l.concurrent[agentKey]--
	}
	if l.totalConc > 0 {
		l.totalConc--
	}
}

// Stats returns current limiter state (for metrics/status).
type Stats struct {
	ConcurrentTotal   int
	ConcurrentByAgent map[string]int
	RunsLastHour      int
}

// GetStats returns current limiter statistics.
func (l *Limiter) GetStats() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneHistory(time.Now())

	byAgent := make(map[string]int, len(l.concurrent))
	for k, v := range l.concurrent {
		byAgent[k] = v
	}

	return Stats{
		ConcurrentTotal:   l.totalConc,
		ConcurrentByAgent: byAgent,
		RunsLastHour:      len(l.history),
	}
}

// pruneHistory removes records older than 1 hour.
func (l *Limiter) pruneHistory(now time.Time) {
	cutoff := now.Add(-1 * time.Hour)
	i := 0
	for i < len(l.history) && l.history[i].time.Before(cutoff) {
		i++
	}
	if i > 0 {
		l.history = l.history[i:]
	}
}

// countAgent counts how many runs this agent has in the history window.
func (l *Limiter) countAgent(agentKey string, now time.Time) int {
	count := 0
	cutoff := now.Add(-1 * time.Hour)
	for _, r := range l.history {
		if r.agentKey == agentKey && !r.time.Before(cutoff) {
			count++
		}
	}
	return count
}
