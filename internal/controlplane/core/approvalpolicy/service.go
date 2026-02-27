package approvalpolicy

import (
	"errors"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/policy"
	"github.com/marcus-qen/legator/internal/protocol"
)

var (
	ErrProbeNotFound          = errors.New("probe not found")
	ErrPolicyTemplateNotFound = errors.New("policy template not found")
)

type approvalQueue interface {
	Submit(probeID string, cmd *protocol.CommandPayload, reason, riskLevel, requester string) (*approval.Request, error)
	Decide(id string, decision approval.Decision, decidedBy string) (*approval.Request, error)
	WaitForDecision(id string, timeout time.Duration) (*approval.Request, error)
}

type fleetStore interface {
	Get(id string) (*fleet.ProbeState, bool)
	SetPolicy(id string, level protocol.CapabilityLevel) error
}

type policyStore interface {
	Get(id string) (*policy.Template, bool)
}

type DecisionHookStage string

const (
	DecisionHookStageDecisionRecorded DecisionHookStage = "decision_recorded"
	DecisionHookStageDispatchComplete DecisionHookStage = "dispatch_complete"
)

// DecisionHookError is returned when a decision hook fails.
type DecisionHookError struct {
	Stage DecisionHookStage
	Err   error
}

func (e *DecisionHookError) Error() string {
	if e == nil || e.Err == nil {
		return "approval decision hook failed"
	}
	return e.Err.Error()
}

func (e *DecisionHookError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ApprovedDispatchError is returned when dispatching an approved command fails.
type ApprovedDispatchError struct {
	Err error
}

func (e *ApprovedDispatchError) Error() string {
	if e == nil || e.Err == nil {
		return "approved dispatch failed"
	}
	return e.Err.Error()
}

func (e *ApprovedDispatchError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// DecisionHooks captures side effects emitted during approval decisions.
type DecisionHooks interface {
	OnDecisionRecorded(result *ApprovalDecisionResult) error
	OnApprovedDispatch(result *ApprovalDecisionResult) error
}

// DecisionHookFuncs adapts function callbacks to DecisionHooks.
type DecisionHookFuncs struct {
	OnDecisionRecordedFn func(result *ApprovalDecisionResult) error
	OnApprovedDispatchFn func(result *ApprovalDecisionResult) error
}

func (h DecisionHookFuncs) OnDecisionRecorded(result *ApprovalDecisionResult) error {
	if h.OnDecisionRecordedFn == nil {
		return nil
	}
	return h.OnDecisionRecordedFn(result)
}

func (h DecisionHookFuncs) OnApprovedDispatch(result *ApprovalDecisionResult) error {
	if h.OnApprovedDispatchFn == nil {
		return nil
	}
	return h.OnApprovedDispatchFn(result)
}

type noopDecisionHooks struct{}

func (noopDecisionHooks) OnDecisionRecorded(*ApprovalDecisionResult) error { return nil }
func (noopDecisionHooks) OnApprovedDispatch(*ApprovalDecisionResult) error { return nil }

// Service orchestrates command approvals and policy application.
type Service struct {
	approvals     approvalQueue
	fleet         fleetStore
	policies      policyStore
	decisionHooks DecisionHooks
}

type Option func(*Service)

func WithDecisionHooks(hooks DecisionHooks) Option {
	return func(s *Service) {
		if hooks != nil {
			s.decisionHooks = hooks
		}
	}
}

func NewService(approvals approvalQueue, fleet fleetStore, policies policyStore, opts ...Option) *Service {
	svc := &Service{
		approvals:     approvals,
		fleet:         fleet,
		policies:      policies,
		decisionHooks: noopDecisionHooks{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(svc)
		}
	}
	return svc
}

func (s *Service) SubmitCommandApproval(probeID string, cmd *protocol.CommandPayload, probeLevel protocol.CapabilityLevel, reason, requester string) (*approval.Request, bool, error) {
	if !approval.NeedsApproval(cmd, probeLevel) {
		return nil, false, nil
	}

	risk := approval.ClassifyRisk(cmd)
	req, err := s.approvals.Submit(probeID, cmd, reason, risk, requester)
	if err != nil {
		return nil, true, err
	}

	return req, true, nil
}

type ApprovalDecisionResult struct {
	Request          *approval.Request
	RequiresDispatch bool
}

func (s *Service) DecideApproval(id string, decision approval.Decision, decidedBy string) (*ApprovalDecisionResult, error) {
	req, err := s.approvals.Decide(id, decision, decidedBy)
	if err != nil {
		return nil, err
	}

	return &ApprovalDecisionResult{
		Request:          req,
		RequiresDispatch: req.Decision == approval.DecisionApproved && req.Command != nil,
	}, nil
}

func (s *Service) DispatchApprovedCommand(decision *ApprovalDecisionResult, dispatch func(probeID string, cmd protocol.CommandPayload) error) error {
	if decision == nil || !decision.RequiresDispatch || decision.Request == nil || dispatch == nil {
		return nil
	}
	return dispatch(decision.Request.ProbeID, *decision.Request.Command)
}

// DecideAndDispatch applies a decision and executes side-effects in a stable order:
//  1. decision recorded hook
//  2. approved dispatch (if required)
//  3. approved-dispatch hook (if dispatch succeeded)
func (s *Service) DecideAndDispatch(id string, decision approval.Decision, decidedBy string, dispatch func(probeID string, cmd protocol.CommandPayload) error) (*ApprovalDecisionResult, error) {
	result, err := s.DecideApproval(id, decision, decidedBy)
	if err != nil {
		return nil, err
	}

	if err := s.decisionHooks.OnDecisionRecorded(result); err != nil {
		return result, &DecisionHookError{Stage: DecisionHookStageDecisionRecorded, Err: err}
	}

	dispatchAttempted := result != nil && result.RequiresDispatch && result.Request != nil && dispatch != nil
	if err := s.DispatchApprovedCommand(result, dispatch); err != nil {
		return result, &ApprovedDispatchError{Err: err}
	}

	if dispatchAttempted {
		if err := s.decisionHooks.OnApprovedDispatch(result); err != nil {
			return result, &DecisionHookError{Stage: DecisionHookStageDispatchComplete, Err: err}
		}
	}

	return result, nil
}

func (s *Service) WaitForDecision(id string, timeout time.Duration) (*approval.Request, error) {
	return s.approvals.WaitForDecision(id, timeout)
}

type PolicyApplyResult struct {
	Template *policy.Template
	Pushed   bool
}

func (s *Service) ApplyPolicyTemplate(probeID, policyID string, push func(probeID string, pol *protocol.PolicyUpdatePayload) error) (*PolicyApplyResult, error) {
	if _, ok := s.fleet.Get(probeID); !ok {
		return nil, ErrProbeNotFound
	}

	tpl, ok := s.policies.Get(policyID)
	if !ok {
		return nil, ErrPolicyTemplateNotFound
	}

	_ = s.fleet.SetPolicy(probeID, tpl.Level)

	if push != nil {
		if err := push(probeID, tpl.ToPolicy()); err != nil {
			return &PolicyApplyResult{Template: tpl, Pushed: false}, nil
		}
	}

	return &PolicyApplyResult{Template: tpl, Pushed: true}, nil
}
