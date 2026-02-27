package approvalpolicy

import (
	"errors"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/policy"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func newServiceForTest() (*Service, *approval.Queue, *fleet.Manager, *policy.Store) {
	queue := approval.NewQueue(15*time.Minute, 16)
	fleetMgr := fleet.NewManager(zap.NewNop())
	policies := policy.NewStore()
	return NewService(queue, fleetMgr, policies), queue, fleetMgr, policies
}

func TestSubmitCommandApproval_NotRequired(t *testing.T) {
	svc, queue, _, _ := newServiceForTest()

	cmd := &protocol.CommandPayload{RequestID: "req-1", Command: "ls", Level: protocol.CapObserve}
	req, needed, err := svc.SubmitCommandApproval("probe-a", cmd, protocol.CapObserve, "manual", "api")
	if err != nil {
		t.Fatalf("SubmitCommandApproval returned error: %v", err)
	}
	if needed {
		t.Fatal("expected needsApproval=false")
	}
	if req != nil {
		t.Fatalf("expected nil approval request, got %+v", req)
	}
	if queue.PendingCount() != 0 {
		t.Fatalf("expected no pending approvals, got %d", queue.PendingCount())
	}
}

func TestSubmitCommandApproval_Required(t *testing.T) {
	svc, queue, _, _ := newServiceForTest()

	cmd := &protocol.CommandPayload{RequestID: "req-2", Command: "systemctl restart nginx", Level: protocol.CapRemediate}
	req, needed, err := svc.SubmitCommandApproval("probe-a", cmd, protocol.CapRemediate, "manual", "api")
	if err != nil {
		t.Fatalf("SubmitCommandApproval returned error: %v", err)
	}
	if !needed {
		t.Fatal("expected needsApproval=true")
	}
	if req == nil {
		t.Fatal("expected approval request")
	}
	if req.RiskLevel != "high" {
		t.Fatalf("expected risk=high, got %s", req.RiskLevel)
	}
	if queue.PendingCount() != 1 {
		t.Fatalf("expected 1 pending approval, got %d", queue.PendingCount())
	}
}

func TestWaitForDecision(t *testing.T) {
	svc, queue, _, _ := newServiceForTest()

	req, err := queue.Submit("probe-a", &protocol.CommandPayload{RequestID: "req-3", Command: "systemctl restart nginx"}, "manual", "high", "api")
	if err != nil {
		t.Fatalf("submit approval: %v", err)
	}
	if _, err := queue.Decide(req.ID, approval.DecisionApproved, "operator"); err != nil {
		t.Fatalf("decide approval: %v", err)
	}

	decided, err := svc.WaitForDecision(req.ID, 2*time.Second)
	if err != nil {
		t.Fatalf("WaitForDecision returned error: %v", err)
	}
	if decided.Decision != approval.DecisionApproved {
		t.Fatalf("expected approved decision, got %s", decided.Decision)
	}
}

func TestApplyPolicyTemplate(t *testing.T) {
	svc, _, fleetMgr, _ := newServiceForTest()

	if _, err := svc.ApplyPolicyTemplate("missing", "observe-only", nil); !errors.Is(err, ErrProbeNotFound) {
		t.Fatalf("expected ErrProbeNotFound, got %v", err)
	}

	fleetMgr.Register("probe-a", "host", "linux", "amd64")

	if _, err := svc.ApplyPolicyTemplate("probe-a", "missing", nil); !errors.Is(err, ErrPolicyTemplateNotFound) {
		t.Fatalf("expected ErrPolicyTemplateNotFound, got %v", err)
	}

	result, err := svc.ApplyPolicyTemplate("probe-a", "observe-only", func(probeID string, pol *protocol.PolicyUpdatePayload) error {
		return errors.New("not connected")
	})
	if err != nil {
		t.Fatalf("ApplyPolicyTemplate returned error: %v", err)
	}
	if result.Pushed {
		t.Fatal("expected push=false on transport failure")
	}
	if result.Template == nil || result.Template.ID != "observe-only" {
		t.Fatalf("unexpected template in result: %+v", result.Template)
	}

	pushCalled := false
	result, err = svc.ApplyPolicyTemplate("probe-a", "diagnose", func(probeID string, pol *protocol.PolicyUpdatePayload) error {
		pushCalled = true
		if probeID != "probe-a" {
			t.Fatalf("unexpected probe id: %s", probeID)
		}
		if pol.PolicyID != "diagnose" {
			t.Fatalf("unexpected policy payload id: %s", pol.PolicyID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ApplyPolicyTemplate returned error: %v", err)
	}
	if !pushCalled {
		t.Fatal("expected push callback to be called")
	}
	if !result.Pushed {
		t.Fatal("expected push=true")
	}
}
