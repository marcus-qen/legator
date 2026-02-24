package blastradius

import (
	"testing"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
)

func TestDeterministicScorer_Assess_Deterministic(t *testing.T) {
	scorer := NewDeterministicScorer()
	in := Input{
		Tier:          corev1alpha1.ActionTierServiceMutation,
		MutationDepth: MutationDepthService,
		ActorRoles:    []string{"operator"},
		Targets: []Target{
			{Kind: "host", Name: "app-1", Environment: "prod", Domain: "ssh"},
		},
	}

	a := scorer.Assess(in)
	b := scorer.Assess(in)

	if a.Radius.Score != b.Radius.Score {
		t.Fatalf("score should be deterministic: got %v and %v", a.Radius.Score, b.Radius.Score)
	}
	if a.Radius.Level != b.Radius.Level {
		t.Fatalf("level should be deterministic: got %v and %v", a.Radius.Level, b.Radius.Level)
	}
	if a.Decision != b.Decision {
		t.Fatalf("decision should be deterministic: got %v and %v", a.Decision, b.Decision)
	}
}

func TestDeterministicScorer_Assess_CriticalNonAdminDenied(t *testing.T) {
	scorer := NewDeterministicScorer()

	result := scorer.Assess(Input{
		Tier:          corev1alpha1.ActionTierDataMutation,
		MutationDepth: MutationDepthIdentity,
		ActorRoles:    []string{"operator"},
		Targets: []Target{
			{Kind: "db", Name: "prod-a", Environment: "prod", Domain: "sql"},
			{Kind: "db", Name: "prod-b", Environment: "prod", Domain: "sql"},
		},
	})

	if result.Radius.Level != LevelCritical {
		t.Fatalf("expected critical level, got %s (score %.2f)", result.Radius.Level, result.Radius.Score)
	}
	if result.Decision != DecisionDeny {
		t.Fatalf("expected deny decision for critical non-admin, got %s", result.Decision)
	}
	if result.Requirements.MaxAllowed {
		t.Fatal("expected maxAllowed=false for critical non-admin")
	}
	if !result.Requirements.TypedConfirmation {
		t.Fatal("expected typed confirmation for critical action")
	}
}

func TestDeterministicScorer_Assess_ReadLowAllowed(t *testing.T) {
	scorer := NewDeterministicScorer()

	result := scorer.Assess(Input{
		Tier:          corev1alpha1.ActionTierRead,
		MutationDepth: MutationDepthService,
		ActorRoles:    []string{"viewer"},
		Targets: []Target{
			{Kind: "host", Name: "dev-a", Environment: "dev", Domain: "ssh"},
		},
	})

	if result.Radius.Level != LevelLow {
		t.Fatalf("expected low level, got %s (score %.2f)", result.Radius.Level, result.Radius.Score)
	}
	if result.Decision != DecisionAllowWithGuards {
		t.Fatalf("expected allow_with_guards decision, got %s", result.Decision)
	}
	if result.Requirements.ApprovalRequired {
		t.Fatal("read low-risk action should not require approval")
	}
	if result.Requirements.TypedConfirmation {
		t.Fatal("read low-risk action should not require typed confirmation")
	}
}

func TestDeterministicScorer_Assess_HighAdminAllowed(t *testing.T) {
	scorer := NewDeterministicScorer()

	result := scorer.Assess(Input{
		Tier:          corev1alpha1.ActionTierDestructiveMutation,
		MutationDepth: MutationDepthNetwork,
		ActorRoles:    []string{"admin"},
		Targets: []Target{
			{Kind: "fw", Name: "edge-fw", Environment: "prod", Domain: "http"},
		},
	})

	if result.Radius.Level != LevelCritical {
		t.Fatalf("expected critical level, got %s (score %.2f)", result.Radius.Level, result.Radius.Score)
	}
	if result.Decision != DecisionAllowWithGuards {
		t.Fatalf("critical action by admin should be allowed with guards, got %s", result.Decision)
	}
	if !result.Requirements.ApprovalRequired || !result.Requirements.TypedConfirmation {
		t.Fatal("critical action should require approval + typed confirmation")
	}
}
