package policy

import (
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
)

func TestNewStoreHasBuiltins(t *testing.T) {
	s := NewStore()
	list := s.List()
	if len(list) < 3 {
		t.Fatalf("expected at least 3 built-in templates, got %d", len(list))
	}
	obs, ok := s.Get("observe-only")
	if !ok || obs.Level != protocol.CapObserve {
		t.Fatal("observe-only template missing or wrong level")
	}
	if obs.ExecutionClassRequired != protocol.ExecObserveDirect || obs.SandboxRequired || obs.ApprovalMode != protocol.ApprovalNone {
		t.Fatalf("unexpected observe-only v2 defaults: %+v", obs)
	}

	diag, ok := s.Get("diagnose")
	if !ok {
		t.Fatal("diagnose template missing")
	}
	if diag.ExecutionClassRequired != protocol.ExecDiagnoseSandbox || !diag.SandboxRequired || diag.ApprovalMode != protocol.ApprovalMutationGate {
		t.Fatalf("unexpected diagnose v2 defaults: %+v", diag)
	}
}

func TestCreateAndGet(t *testing.T) {
	s := NewStore()
	tpl := s.Create("Custom", "A custom policy", protocol.CapDiagnose, nil, []string{"rm"}, nil, TemplateOptions{})
	if tpl.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	got, ok := s.Get(tpl.ID)
	if !ok {
		t.Fatal("template not found after create")
	}
	if got.Name != "Custom" || got.Level != protocol.CapDiagnose {
		t.Fatalf("unexpected: %#v", got)
	}
	if got.ExecutionClassRequired != protocol.ExecDiagnoseSandbox || !got.SandboxRequired || got.ApprovalMode != protocol.ApprovalMutationGate {
		t.Fatalf("expected level-derived v2 defaults, got %+v", got)
	}
}

func TestUpdate(t *testing.T) {
	s := NewStore()
	tpl := s.Create("Test", "desc", protocol.CapObserve, nil, nil, nil, TemplateOptions{})
	updated, err := s.Update(tpl.ID, "Test v2", "new desc", protocol.CapRemediate, []string{"ls"}, []string{"rm"}, []string{"/etc"}, TemplateOptions{
		ExecutionClassRequired:   protocol.ExecBreakglassDirect,
		SandboxRequired:          true,
		ApprovalMode:             protocol.ApprovalEveryAction,
		RequireSecondApprover:    true,
		RequireSecondApproverSet: true,
		Breakglass: protocol.BreakglassPolicy{
			Enabled:                  true,
			AllowedReasons:           []string{"incident_response", "incident_response"},
			RequireTypedConfirmation: true,
		},
		MaxRuntimeSec: 600,
		AllowedScopes: []string{"fleet.read", "fleet.read", " command.exec "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Test v2" || updated.Level != protocol.CapRemediate {
		t.Fatalf("update failed: %#v", updated)
	}
	if updated.ExecutionClassRequired != protocol.ExecBreakglassDirect || updated.ApprovalMode != protocol.ApprovalEveryAction {
		t.Fatalf("expected v2 override fields, got %+v", updated)
	}
	if !updated.RequireSecondApprover {
		t.Fatalf("expected require_second_approver override, got %+v", updated)
	}
	if len(updated.Breakglass.AllowedReasons) != 1 || updated.Breakglass.AllowedReasons[0] != "incident_response" {
		t.Fatalf("expected normalized breakglass reasons, got %+v", updated.Breakglass)
	}
	if len(updated.AllowedScopes) != 2 || updated.AllowedScopes[1] != "command.exec" {
		t.Fatalf("expected normalized allowed scopes, got %v", updated.AllowedScopes)
	}
}

func TestUpdateNotFound(t *testing.T) {
	s := NewStore()
	_, err := s.Update("nonexistent", "", "", "", nil, nil, nil, TemplateOptions{})
	if err == nil {
		t.Fatal("expected error for missing template")
	}
}

func TestDelete(t *testing.T) {
	s := NewStore()
	tpl := s.Create("Temp", "", protocol.CapObserve, nil, nil, nil, TemplateOptions{})
	if err := s.Delete(tpl.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(tpl.ID); ok {
		t.Fatal("template should be deleted")
	}
}

func TestDeleteNotFound(t *testing.T) {
	s := NewStore()
	if err := s.Delete("nope"); err == nil {
		t.Fatal("expected error for missing template")
	}
}

func TestToPolicy(t *testing.T) {
	s := NewStore()
	obs, _ := s.Get("observe-only")
	pol := obs.ToPolicy()
	if pol.PolicyID != "observe-only" || pol.Level != protocol.CapObserve {
		t.Fatalf("unexpected policy: %#v", pol)
	}
	if len(pol.Blocked) == 0 {
		t.Fatal("expected blocked commands from observe-only")
	}
	if pol.ExecutionClassRequired != protocol.ExecObserveDirect || pol.ApprovalMode != protocol.ApprovalNone {
		t.Fatalf("expected v2 fields in policy payload: %+v", pol)
	}
	if pol.RequireSecondApprover {
		t.Fatalf("expected require_second_approver false by default: %+v", pol)
	}
}
