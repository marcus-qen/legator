/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tenant

import (
	"testing"

	"github.com/go-logr/logr"
)

func newEnforcer() *QuotaEnforcer {
	return NewQuotaEnforcer(logr.Discard())
}

func TestQuotaEnforcer_NoQuotas(t *testing.T) {
	qe := newEnforcer()

	// No team registered = no limits
	if err := qe.CheckCanCreateAgent("unknown"); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if err := qe.CheckCanStartRun("unknown"); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestQuotaEnforcer_MaxAgents(t *testing.T) {
	qe := newEnforcer()
	qe.RegisterTeam(Team{
		Name:      "platform",
		Namespace: "legator-platform",
		Quotas:    TeamQuotas{MaxAgents: 3},
	})

	// Under limit
	qe.RecordAgentCreated("platform")
	qe.RecordAgentCreated("platform")
	if err := qe.CheckCanCreateAgent("platform"); err != nil {
		t.Errorf("expected allowed, got: %v", err)
	}

	// At limit
	qe.RecordAgentCreated("platform")
	if err := qe.CheckCanCreateAgent("platform"); err == nil {
		t.Error("expected error at max agents")
	}

	// Delete one, should be allowed again
	qe.RecordAgentDeleted("platform")
	if err := qe.CheckCanCreateAgent("platform"); err != nil {
		t.Errorf("expected allowed after delete, got: %v", err)
	}
}

func TestQuotaEnforcer_MaxConcurrentRuns(t *testing.T) {
	qe := newEnforcer()
	qe.RegisterTeam(Team{
		Name:   "data",
		Quotas: TeamQuotas{MaxConcurrentRuns: 2},
	})

	qe.RecordRunStart("data")
	qe.RecordRunStart("data")

	if err := qe.CheckCanStartRun("data"); err == nil {
		t.Error("expected error at max concurrent runs")
	}

	qe.RecordRunEnd("data", 5000)
	if err := qe.CheckCanStartRun("data"); err != nil {
		t.Errorf("expected allowed after run end, got: %v", err)
	}
}

func TestQuotaEnforcer_MaxRunsPerDay(t *testing.T) {
	qe := newEnforcer()
	qe.RegisterTeam(Team{
		Name:   "testing",
		Quotas: TeamQuotas{MaxRunsPerDay: 5},
	})

	for i := 0; i < 5; i++ {
		qe.RecordRunStart("testing")
		qe.RecordRunEnd("testing", 1000)
	}

	if err := qe.CheckCanStartRun("testing"); err == nil {
		t.Error("expected error at max runs per day")
	}

	qe.ResetDailyUsage()
	if err := qe.CheckCanStartRun("testing"); err != nil {
		t.Errorf("expected allowed after daily reset, got: %v", err)
	}
}

func TestQuotaEnforcer_TokenBudget(t *testing.T) {
	qe := newEnforcer()
	qe.RegisterTeam(Team{
		Name:   "analytics",
		Quotas: TeamQuotas{MaxTokenBudgetPerHour: 100000},
	})

	qe.RecordRunStart("analytics")
	qe.RecordRunEnd("analytics", 80000)

	if err := qe.CheckCanStartRun("analytics"); err != nil {
		t.Errorf("expected allowed under budget, got: %v", err)
	}

	qe.RecordRunStart("analytics")
	qe.RecordRunEnd("analytics", 30000)

	if err := qe.CheckCanStartRun("analytics"); err == nil {
		t.Error("expected error over token budget")
	}

	qe.ResetHourlyUsage()
	if err := qe.CheckCanStartRun("analytics"); err != nil {
		t.Errorf("expected allowed after hourly reset, got: %v", err)
	}
}

func TestQuotaEnforcer_CostReport(t *testing.T) {
	qe := newEnforcer()
	qe.RegisterTeam(Team{
		Name:   "platform",
		Quotas: TeamQuotas{MaxAgents: 10, MaxTokenBudgetPerHour: 500000},
	})

	qe.RecordAgentCreated("platform")
	qe.RecordAgentCreated("platform")
	qe.RecordRunStart("platform")
	qe.RecordRunEnd("platform", 15000)

	report, err := qe.CostReport("platform")
	if err != nil {
		t.Fatalf("CostReport error: %v", err)
	}
	if report.ActiveAgents != 2 {
		t.Errorf("activeAgents = %d, want 2", report.ActiveAgents)
	}
	if report.TokensThisHour != 15000 {
		t.Errorf("tokensThisHour = %d, want 15000", report.TokensThisHour)
	}
	if report.TokensAllTime != 15000 {
		t.Errorf("tokensAllTime = %d, want 15000", report.TokensAllTime)
	}
}

func TestQuotaEnforcer_CostReport_NotFound(t *testing.T) {
	qe := newEnforcer()
	_, err := qe.CostReport("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent team")
	}
}

func TestQuotaEnforcer_GetTeam(t *testing.T) {
	qe := newEnforcer()
	qe.RegisterTeam(Team{Name: "platform", Namespace: "legator-platform"})

	team, ok := qe.GetTeam("platform")
	if !ok {
		t.Fatal("expected team to be found")
	}
	if team.Name != "platform" {
		t.Errorf("name = %q, want platform", team.Name)
	}

	_, ok = qe.GetTeam("nonexistent")
	if ok {
		t.Error("expected team not found")
	}
}

func TestNamespaceForTeam(t *testing.T) {
	if ns := NamespaceForTeam("platform"); ns != "legator-platform" {
		t.Errorf("NamespaceForTeam = %q, want legator-platform", ns)
	}
}

func TestTeamForNamespace(t *testing.T) {
	if team := TeamForNamespace("legator-platform"); team != "platform" {
		t.Errorf("TeamForNamespace = %q, want platform", team)
	}
	if team := TeamForNamespace("custom-ns"); team != "custom-ns" {
		t.Errorf("TeamForNamespace = %q, want custom-ns", team)
	}
}

func TestQuotaEnforcer_TeamIsolation(t *testing.T) {
	qe := newEnforcer()
	qe.RegisterTeam(Team{Name: "team-a", Quotas: TeamQuotas{MaxAgents: 2}})
	qe.RegisterTeam(Team{Name: "team-b", Quotas: TeamQuotas{MaxAgents: 2}})

	// Fill team-a
	qe.RecordAgentCreated("team-a")
	qe.RecordAgentCreated("team-a")

	// team-a should be blocked
	if err := qe.CheckCanCreateAgent("team-a"); err == nil {
		t.Error("team-a should be at quota")
	}

	// team-b should still be fine
	if err := qe.CheckCanCreateAgent("team-b"); err != nil {
		t.Errorf("team-b should be allowed: %v", err)
	}
}
