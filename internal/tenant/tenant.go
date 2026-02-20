/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package tenant provides multi-tenant foundations for Legator.
// Teams are isolated by namespace. Each team has:
//   - Resource quotas (max agents, concurrent runs, token budget)
//   - Cost attribution (token usage tracked per team)
//   - Scoped RBAC (team members can only see their agents)
package tenant

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// Team represents a tenant in the multi-tenant model.
type Team struct {
	// Name is the team identifier (maps to K8s namespace).
	Name string

	// Namespace is the K8s namespace for this team's agents.
	Namespace string

	// Quotas define resource limits for this team.
	Quotas TeamQuotas

	// Usage tracks current resource consumption.
	Usage TeamUsage
}

// TeamQuotas defines resource limits per team.
type TeamQuotas struct {
	// MaxAgents is the maximum number of agents this team can create.
	MaxAgents int `json:"maxAgents"`

	// MaxConcurrentRuns is the maximum simultaneous runs across all team agents.
	MaxConcurrentRuns int `json:"maxConcurrentRuns"`

	// MaxTokenBudgetPerHour is the aggregate token budget per hour.
	MaxTokenBudgetPerHour int64 `json:"maxTokenBudgetPerHour"`

	// MaxRunsPerDay is the maximum total runs per day.
	MaxRunsPerDay int `json:"maxRunsPerDay"`
}

// TeamUsage tracks current resource consumption.
type TeamUsage struct {
	// ActiveAgents is the current number of agents.
	ActiveAgents int `json:"activeAgents"`

	// ConcurrentRuns is the current number of running runs.
	ConcurrentRuns int `json:"concurrentRuns"`

	// TokensUsedThisHour is the approximate tokens consumed in the current hour.
	TokensUsedThisHour int64 `json:"tokensUsedThisHour"`

	// RunsToday is the number of runs started today.
	RunsToday int `json:"runsToday"`

	// TotalTokensAllTime is the lifetime token consumption.
	TotalTokensAllTime int64 `json:"totalTokensAllTime"`

	// LastUpdated is when usage was last calculated.
	LastUpdated time.Time `json:"lastUpdated"`
}

// QuotaEnforcer checks team quotas before allowing operations.
type QuotaEnforcer struct {
	mu     sync.RWMutex
	teams  map[string]*Team
	log    logr.Logger
}

// NewQuotaEnforcer creates a quota enforcer.
func NewQuotaEnforcer(log logr.Logger) *QuotaEnforcer {
	return &QuotaEnforcer{
		teams: make(map[string]*Team),
		log:   log,
	}
}

// RegisterTeam adds or updates a team's quotas.
func (qe *QuotaEnforcer) RegisterTeam(team Team) {
	qe.mu.Lock()
	defer qe.mu.Unlock()
	qe.teams[team.Name] = &team
}

// GetTeam returns a team by name.
func (qe *QuotaEnforcer) GetTeam(name string) (*Team, bool) {
	qe.mu.RLock()
	defer qe.mu.RUnlock()
	team, ok := qe.teams[name]
	if !ok {
		return nil, false
	}
	// Return a copy
	copy := *team
	return &copy, true
}

// CheckCanCreateAgent verifies the team hasn't exceeded agent quota.
func (qe *QuotaEnforcer) CheckCanCreateAgent(teamName string) error {
	qe.mu.RLock()
	defer qe.mu.RUnlock()

	team, ok := qe.teams[teamName]
	if !ok {
		return nil // no quotas = no limits
	}

	if team.Quotas.MaxAgents > 0 && team.Usage.ActiveAgents >= team.Quotas.MaxAgents {
		return fmt.Errorf("team %q exceeded max agents quota (%d/%d)", teamName, team.Usage.ActiveAgents, team.Quotas.MaxAgents)
	}

	return nil
}

// CheckCanStartRun verifies the team hasn't exceeded run quotas.
func (qe *QuotaEnforcer) CheckCanStartRun(teamName string) error {
	qe.mu.RLock()
	defer qe.mu.RUnlock()

	team, ok := qe.teams[teamName]
	if !ok {
		return nil
	}

	if team.Quotas.MaxConcurrentRuns > 0 && team.Usage.ConcurrentRuns >= team.Quotas.MaxConcurrentRuns {
		return fmt.Errorf("team %q exceeded max concurrent runs (%d/%d)", teamName, team.Usage.ConcurrentRuns, team.Quotas.MaxConcurrentRuns)
	}

	if team.Quotas.MaxRunsPerDay > 0 && team.Usage.RunsToday >= team.Quotas.MaxRunsPerDay {
		return fmt.Errorf("team %q exceeded max runs per day (%d/%d)", teamName, team.Usage.RunsToday, team.Quotas.MaxRunsPerDay)
	}

	if team.Quotas.MaxTokenBudgetPerHour > 0 && team.Usage.TokensUsedThisHour >= team.Quotas.MaxTokenBudgetPerHour {
		return fmt.Errorf("team %q exceeded hourly token budget (%d/%d)", teamName, team.Usage.TokensUsedThisHour, team.Quotas.MaxTokenBudgetPerHour)
	}

	return nil
}

// RecordRunStart increments concurrent run count.
func (qe *QuotaEnforcer) RecordRunStart(teamName string) {
	qe.mu.Lock()
	defer qe.mu.Unlock()

	team, ok := qe.teams[teamName]
	if !ok {
		return
	}
	team.Usage.ConcurrentRuns++
	team.Usage.RunsToday++
	team.Usage.LastUpdated = time.Now()
}

// RecordRunEnd decrements concurrent run count and adds token usage.
func (qe *QuotaEnforcer) RecordRunEnd(teamName string, tokensUsed int64) {
	qe.mu.Lock()
	defer qe.mu.Unlock()

	team, ok := qe.teams[teamName]
	if !ok {
		return
	}
	if team.Usage.ConcurrentRuns > 0 {
		team.Usage.ConcurrentRuns--
	}
	team.Usage.TokensUsedThisHour += tokensUsed
	team.Usage.TotalTokensAllTime += tokensUsed
	team.Usage.LastUpdated = time.Now()
}

// RecordAgentCreated increments agent count.
func (qe *QuotaEnforcer) RecordAgentCreated(teamName string) {
	qe.mu.Lock()
	defer qe.mu.Unlock()

	team, ok := qe.teams[teamName]
	if !ok {
		return
	}
	team.Usage.ActiveAgents++
}

// RecordAgentDeleted decrements agent count.
func (qe *QuotaEnforcer) RecordAgentDeleted(teamName string) {
	qe.mu.Lock()
	defer qe.mu.Unlock()

	team, ok := qe.teams[teamName]
	if !ok {
		return
	}
	if team.Usage.ActiveAgents > 0 {
		team.Usage.ActiveAgents--
	}
}

// ResetHourlyUsage resets the hourly token counter for all teams.
// Should be called by a periodic job.
func (qe *QuotaEnforcer) ResetHourlyUsage() {
	qe.mu.Lock()
	defer qe.mu.Unlock()

	for _, team := range qe.teams {
		team.Usage.TokensUsedThisHour = 0
	}
}

// ResetDailyUsage resets the daily run counter for all teams.
func (qe *QuotaEnforcer) ResetDailyUsage() {
	qe.mu.Lock()
	defer qe.mu.Unlock()

	for _, team := range qe.teams {
		team.Usage.RunsToday = 0
	}
}

// CostReport generates a cost summary for a team.
func (qe *QuotaEnforcer) CostReport(teamName string) (*CostReport, error) {
	qe.mu.RLock()
	defer qe.mu.RUnlock()

	team, ok := qe.teams[teamName]
	if !ok {
		return nil, fmt.Errorf("team %q not found", teamName)
	}

	return &CostReport{
		TeamName:           team.Name,
		ActiveAgents:       team.Usage.ActiveAgents,
		RunsToday:          team.Usage.RunsToday,
		TokensThisHour:     team.Usage.TokensUsedThisHour,
		TokensAllTime:      team.Usage.TotalTokensAllTime,
		ConcurrentRuns:     team.Usage.ConcurrentRuns,
		QuotaAgents:        team.Quotas.MaxAgents,
		QuotaConcurrent:    team.Quotas.MaxConcurrentRuns,
		QuotaTokensPerHour: team.Quotas.MaxTokenBudgetPerHour,
		QuotaRunsPerDay:    team.Quotas.MaxRunsPerDay,
	}, nil
}

// CostReport is a snapshot of team resource usage.
type CostReport struct {
	TeamName           string `json:"team"`
	ActiveAgents       int    `json:"activeAgents"`
	RunsToday          int    `json:"runsToday"`
	TokensThisHour     int64  `json:"tokensThisHour"`
	TokensAllTime      int64  `json:"tokensAllTime"`
	ConcurrentRuns     int    `json:"concurrentRuns"`
	QuotaAgents        int    `json:"quotaAgents"`
	QuotaConcurrent    int    `json:"quotaConcurrent"`
	QuotaTokensPerHour int64  `json:"quotaTokensPerHour"`
	QuotaRunsPerDay    int    `json:"quotaRunsPerDay"`
}

// NamespaceForTeam returns the namespace for a team.
// Convention: team namespace = "legator-<teamname>"
func NamespaceForTeam(teamName string) string {
	return "legator-" + teamName
}

// TeamForNamespace extracts team name from namespace.
func TeamForNamespace(namespace string) string {
	if len(namespace) > 8 && namespace[:8] == "legator-" {
		return namespace[8:]
	}
	return namespace
}
