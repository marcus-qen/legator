package approvalpolicy

import (
	"errors"
	"reflect"
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

func TestDecideApproval(t *testing.T) {
	svc, queue, _, _ := newServiceForTest()

	req, err := queue.Submit("probe-a", &protocol.CommandPayload{RequestID: "req-4", Command: "systemctl restart nginx"}, "manual", "high", "api")
	if err != nil {
		t.Fatalf("submit approval: %v", err)
	}

	decision, err := svc.DecideApproval(req.ID, approval.DecisionDenied, "operator")
	if err != nil {
		t.Fatalf("DecideApproval returned error: %v", err)
	}
	if decision == nil || decision.Request == nil {
		t.Fatal("expected decision result with request")
	}
	if decision.Request.Decision != approval.DecisionDenied {
		t.Fatalf("expected denied decision, got %s", decision.Request.Decision)
	}
	if decision.RequiresDispatch {
		t.Fatal("expected RequiresDispatch=false for denied decision")
	}
}

func TestDispatchApprovedCommand(t *testing.T) {
	svc, queue, _, _ := newServiceForTest()

	req, err := queue.Submit("probe-a", &protocol.CommandPayload{RequestID: "req-5", Command: "systemctl restart nginx"}, "manual", "high", "api")
	if err != nil {
		t.Fatalf("submit approval: %v", err)
	}
	decision, err := svc.DecideApproval(req.ID, approval.DecisionApproved, "operator")
	if err != nil {
		t.Fatalf("DecideApproval returned error: %v", err)
	}

	called := false
	err = svc.DispatchApprovedCommand(decision, func(probeID string, cmd protocol.CommandPayload) error {
		called = true
		if probeID != "probe-a" {
			t.Fatalf("unexpected probeID: %s", probeID)
		}
		if cmd.RequestID != "req-5" {
			t.Fatalf("unexpected command payload: %+v", cmd)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("DispatchApprovedCommand returned error: %v", err)
	}
	if !called {
		t.Fatal("expected dispatch callback to be called")
	}

	err = svc.DispatchApprovedCommand(decision, func(string, protocol.CommandPayload) error {
		return errors.New("not connected")
	})
	if err == nil {
		t.Fatal("expected dispatch error")
	}
}

func TestDecideAndDispatch_HookOrder(t *testing.T) {
	queue := approval.NewQueue(15*time.Minute, 16)
	fleetMgr := fleet.NewManager(zap.NewNop())
	policies := policy.NewStore()

	order := make([]string, 0, 3)
	svc := NewService(queue, fleetMgr, policies, WithDecisionHooks(DecisionHookFuncs{
		OnDecisionRecordedFn: func(*ApprovalDecisionResult) error {
			order = append(order, "hook:decision")
			return nil
		},
		OnApprovedDispatchFn: func(*ApprovalDecisionResult) error {
			order = append(order, "hook:dispatch")
			return nil
		},
	}))

	req, err := queue.Submit("probe-a", &protocol.CommandPayload{RequestID: "req-order", Command: "systemctl restart nginx"}, "manual", "high", "api")
	if err != nil {
		t.Fatalf("submit approval: %v", err)
	}

	result, err := svc.DecideAndDispatch(req.ID, approval.DecisionApproved, "operator", func(probeID string, cmd protocol.CommandPayload) error {
		order = append(order, "dispatch")
		if probeID != "probe-a" || cmd.RequestID != "req-order" {
			t.Fatalf("unexpected dispatch args: probe=%s cmd=%+v", probeID, cmd)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("DecideAndDispatch returned error: %v", err)
	}
	if result == nil || result.Request == nil {
		t.Fatal("expected decision result")
	}

	want := []string{"hook:decision", "dispatch", "hook:dispatch"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("unexpected invocation order: got=%v want=%v", order, want)
	}
}

func TestDecideAndDispatch_DeniedSkipsDispatchHook(t *testing.T) {
	queue := approval.NewQueue(15*time.Minute, 16)
	fleetMgr := fleet.NewManager(zap.NewNop())
	policies := policy.NewStore()

	order := make([]string, 0, 2)
	svc := NewService(queue, fleetMgr, policies, WithDecisionHooks(DecisionHookFuncs{
		OnDecisionRecordedFn: func(*ApprovalDecisionResult) error {
			order = append(order, "hook:decision")
			return nil
		},
		OnApprovedDispatchFn: func(*ApprovalDecisionResult) error {
			order = append(order, "hook:dispatch")
			return nil
		},
	}))

	req, err := queue.Submit("probe-a", &protocol.CommandPayload{RequestID: "req-denied", Command: "systemctl restart nginx"}, "manual", "high", "api")
	if err != nil {
		t.Fatalf("submit approval: %v", err)
	}

	dispatchCalled := false
	result, err := svc.DecideAndDispatch(req.ID, approval.DecisionDenied, "operator", func(string, protocol.CommandPayload) error {
		dispatchCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("DecideAndDispatch returned error: %v", err)
	}
	if dispatchCalled {
		t.Fatal("expected denied decision to skip dispatch")
	}
	if result == nil || result.Request == nil || result.Request.Decision != approval.DecisionDenied {
		t.Fatalf("unexpected decision result: %+v", result)
	}

	want := []string{"hook:decision"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("unexpected invocation order: got=%v want=%v", order, want)
	}
}

func TestDecideAndDispatch_FailureSemantics(t *testing.T) {
	t.Run("decision hook failure stops before dispatch", func(t *testing.T) {
		queue := approval.NewQueue(15*time.Minute, 16)
		fleetMgr := fleet.NewManager(zap.NewNop())
		policies := policy.NewStore()

		hookFailure := errors.New("audit down")
		svc := NewService(queue, fleetMgr, policies, WithDecisionHooks(DecisionHookFuncs{
			OnDecisionRecordedFn: func(*ApprovalDecisionResult) error { return hookFailure },
		}))

		req, err := queue.Submit("probe-a", &protocol.CommandPayload{RequestID: "req-hook-fail", Command: "systemctl restart nginx"}, "manual", "high", "api")
		if err != nil {
			t.Fatalf("submit approval: %v", err)
		}

		dispatchCalled := false
		result, err := svc.DecideAndDispatch(req.ID, approval.DecisionApproved, "operator", func(string, protocol.CommandPayload) error {
			dispatchCalled = true
			return nil
		})
		if err == nil {
			t.Fatal("expected hook error")
		}
		var hookErr *DecisionHookError
		if !errors.As(err, &hookErr) {
			t.Fatalf("expected DecisionHookError, got %T", err)
		}
		if hookErr.Stage != DecisionHookStageDecisionRecorded {
			t.Fatalf("expected decision-recorded stage, got %s", hookErr.Stage)
		}
		if !errors.Is(err, hookFailure) {
			t.Fatalf("expected wrapped hook failure, got %v", err)
		}
		if dispatchCalled {
			t.Fatal("dispatch should not run after decision hook failure")
		}
		if result == nil || result.Request == nil || result.Request.Decision != approval.DecisionApproved {
			t.Fatalf("decision should still be recorded before hook failure, got %+v", result)
		}
	})

	t.Run("dispatch failure skips post-dispatch hook", func(t *testing.T) {
		queue := approval.NewQueue(15*time.Minute, 16)
		fleetMgr := fleet.NewManager(zap.NewNop())
		policies := policy.NewStore()

		order := make([]string, 0, 2)
		dispatchFailure := errors.New("not connected")
		svc := NewService(queue, fleetMgr, policies, WithDecisionHooks(DecisionHookFuncs{
			OnDecisionRecordedFn: func(*ApprovalDecisionResult) error {
				order = append(order, "hook:decision")
				return nil
			},
			OnApprovedDispatchFn: func(*ApprovalDecisionResult) error {
				order = append(order, "hook:dispatch")
				return nil
			},
		}))

		req, err := queue.Submit("probe-a", &protocol.CommandPayload{RequestID: "req-dispatch-fail", Command: "systemctl restart nginx"}, "manual", "high", "api")
		if err != nil {
			t.Fatalf("submit approval: %v", err)
		}

		_, err = svc.DecideAndDispatch(req.ID, approval.DecisionApproved, "operator", func(string, protocol.CommandPayload) error {
			order = append(order, "dispatch")
			return dispatchFailure
		})
		if err == nil {
			t.Fatal("expected dispatch error")
		}
		var dispatchErr *ApprovedDispatchError
		if !errors.As(err, &dispatchErr) {
			t.Fatalf("expected ApprovedDispatchError, got %T", err)
		}
		if !errors.Is(err, dispatchFailure) {
			t.Fatalf("expected wrapped dispatch failure, got %v", err)
		}

		want := []string{"hook:decision", "dispatch"}
		if !reflect.DeepEqual(order, want) {
			t.Fatalf("unexpected invocation order: got=%v want=%v", order, want)
		}
	})

	t.Run("post-dispatch hook failure returns hook error", func(t *testing.T) {
		queue := approval.NewQueue(15*time.Minute, 16)
		fleetMgr := fleet.NewManager(zap.NewNop())
		policies := policy.NewStore()

		order := make([]string, 0, 3)
		hookFailure := errors.New("event bus down")
		svc := NewService(queue, fleetMgr, policies, WithDecisionHooks(DecisionHookFuncs{
			OnDecisionRecordedFn: func(*ApprovalDecisionResult) error {
				order = append(order, "hook:decision")
				return nil
			},
			OnApprovedDispatchFn: func(*ApprovalDecisionResult) error {
				order = append(order, "hook:dispatch")
				return hookFailure
			},
		}))

		req, err := queue.Submit("probe-a", &protocol.CommandPayload{RequestID: "req-post-hook-fail", Command: "systemctl restart nginx"}, "manual", "high", "api")
		if err != nil {
			t.Fatalf("submit approval: %v", err)
		}

		_, err = svc.DecideAndDispatch(req.ID, approval.DecisionApproved, "operator", func(string, protocol.CommandPayload) error {
			order = append(order, "dispatch")
			return nil
		})
		if err == nil {
			t.Fatal("expected hook error")
		}
		var hookErr *DecisionHookError
		if !errors.As(err, &hookErr) {
			t.Fatalf("expected DecisionHookError, got %T", err)
		}
		if hookErr.Stage != DecisionHookStageDispatchComplete {
			t.Fatalf("expected dispatch-complete stage, got %s", hookErr.Stage)
		}
		if !errors.Is(err, hookFailure) {
			t.Fatalf("expected wrapped hook failure, got %v", err)
		}

		want := []string{"hook:decision", "dispatch", "hook:dispatch"}
		if !reflect.DeepEqual(order, want) {
			t.Fatalf("unexpected invocation order: got=%v want=%v", order, want)
		}
	})
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
