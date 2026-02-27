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
	WaitForDecision(id string, timeout time.Duration) (*approval.Request, error)
}

type fleetStore interface {
	Get(id string) (*fleet.ProbeState, bool)
	SetPolicy(id string, level protocol.CapabilityLevel) error
}

type policyStore interface {
	Get(id string) (*policy.Template, bool)
}

// Service orchestrates command approvals and policy application.
type Service struct {
	approvals approvalQueue
	fleet     fleetStore
	policies  policyStore
}

func NewService(approvals approvalQueue, fleet fleetStore, policies policyStore) *Service {
	return &Service{
		approvals: approvals,
		fleet:     fleet,
		policies:  policies,
	}
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
