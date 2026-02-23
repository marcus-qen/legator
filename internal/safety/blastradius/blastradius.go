/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package blastradius computes deterministic pre-execution safety assessments
// for human-triggered operations.
package blastradius

import (
	"math"
	"slices"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
)

// Level describes the blast-radius risk band.
type Level string

const (
	LevelLow      Level = "low"
	LevelMedium   Level = "medium"
	LevelHigh     Level = "high"
	LevelCritical Level = "critical"
)

// Decision describes the resulting gate posture.
type Decision string

const (
	DecisionAllowWithGuards Decision = "allow_with_guards"
	DecisionDeny            Decision = "deny"
)

// MutationDepth is the broad class of mutation impact.
type MutationDepth string

const (
	MutationDepthService  MutationDepth = "service"
	MutationDepthData     MutationDepth = "data"
	MutationDepthNetwork  MutationDepth = "network"
	MutationDepthIdentity MutationDepth = "identity"
)

// Target represents one requested target for an operation.
type Target struct {
	Kind        string
	Name        string
	Environment string // dev|staging|prod
	Domain      string // kubernetes|ssh|sql|http|...
}

// Requirements describes required safety gates for a request.
type Requirements struct {
	TypedConfirmation bool `json:"typedConfirmation"`
	ApprovalRequired  bool `json:"approvalRequired"`
	CooldownRequired  bool `json:"cooldownRequired"`
	MaxAllowed        bool `json:"maxAllowed"`
}

// Radius is the quantified blast-radius summary.
type Radius struct {
	Level          Level         `json:"level"`
	Score          float64       `json:"score"`
	TargetCount    int           `json:"targetCount"`
	ProdTargetCount int          `json:"prodTargetCount"`
	CrossDomain    bool          `json:"crossDomain"`
	MutationDepth  MutationDepth `json:"mutationDepth"`
}

// Input contains the request context to evaluate.
type Input struct {
	Tier         corev1alpha1.ActionTier
	Targets      []Target
	MutationDepth MutationDepth
	ActorRoles   []string
}

// Assessment is the computed blast-radius result.
type Assessment struct {
	Radius       Radius       `json:"radius"`
	Requirements Requirements `json:"requirements"`
	Reasons      []string     `json:"reasons"`
	Decision     Decision     `json:"decision"`
}

// Scorer computes blast-radius assessments.
type Scorer interface {
	Assess(Input) Assessment
}

// DeterministicScorer is the v1 deterministic implementation.
type DeterministicScorer struct{}

// NewDeterministicScorer returns the default v1 scorer.
func NewDeterministicScorer() *DeterministicScorer {
	return &DeterministicScorer{}
}

// Assess computes an assessment from static rule weights.
func (s *DeterministicScorer) Assess(in Input) Assessment {
	score := tierWeight(in.Tier) + depthWeight(in.MutationDepth)
	reasons := []string{string(in.Tier)}

	prodTargets := 0
	domains := map[string]struct{}{}
	for _, t := range in.Targets {
		if t.Environment == "prod" {
			prodTargets++
		}
		if t.Domain != "" {
			domains[t.Domain] = struct{}{}
		}
	}

	if prodTargets > 0 {
		reasons = append(reasons, "prod_target")
		score += math.Min(0.30, float64(prodTargets)*0.15)
	}

	if len(in.Targets) > 1 {
		reasons = append(reasons, "multi_target")
		score += math.Min(0.20, float64(len(in.Targets)-1)*0.05)
	}

	crossDomain := len(domains) > 1
	if crossDomain {
		reasons = append(reasons, "cross_domain")
		score += 0.10
	}

	if in.MutationDepth != "" {
		reasons = append(reasons, string(in.MutationDepth)+"_mutation")
	}

	score = clamp(score, 0.0, 1.0)
	level := levelFromScore(score)

	req := Requirements{
		TypedConfirmation: level == LevelHigh || level == LevelCritical || in.Tier == corev1alpha1.ActionTierDestructiveMutation || in.Tier == corev1alpha1.ActionTierDataMutation,
		ApprovalRequired:  level != LevelLow && in.Tier != corev1alpha1.ActionTierRead,
		CooldownRequired:  level == LevelCritical,
		MaxAllowed:        true,
	}

	if level == LevelCritical && !slices.Contains(in.ActorRoles, "admin") {
		req.MaxAllowed = false
		reasons = append(reasons, "critical_non_admin")
	}

	decision := DecisionAllowWithGuards
	if !req.MaxAllowed {
		decision = DecisionDeny
	}

	return Assessment{
		Radius: Radius{
			Level:           level,
			Score:           score,
			TargetCount:     len(in.Targets),
			ProdTargetCount: prodTargets,
			CrossDomain:     crossDomain,
			MutationDepth:   in.MutationDepth,
		},
		Requirements: req,
		Reasons:      reasons,
		Decision:     decision,
	}
}

func tierWeight(t corev1alpha1.ActionTier) float64 {
	switch t {
	case corev1alpha1.ActionTierRead:
		return 0.05
	case corev1alpha1.ActionTierServiceMutation:
		return 0.35
	case corev1alpha1.ActionTierDestructiveMutation:
		return 0.65
	case corev1alpha1.ActionTierDataMutation:
		return 0.75
	default:
		return 0.75 // fail-closed
	}
}

func depthWeight(d MutationDepth) float64 {
	switch d {
	case MutationDepthService:
		return 0.10
	case MutationDepthData:
		return 0.20
	case MutationDepthNetwork:
		return 0.25
	case MutationDepthIdentity:
		return 0.30
	default:
		return 0.0
	}
}

func levelFromScore(score float64) Level {
	switch {
	case score >= 0.80:
		return LevelCritical
	case score >= 0.60:
		return LevelHigh
	case score >= 0.30:
		return LevelMedium
	default:
		return LevelLow
	}
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
