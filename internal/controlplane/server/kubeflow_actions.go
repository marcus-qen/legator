package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/controlplane/events"
	"github.com/marcus-qen/legator/internal/controlplane/kubeflow"
	"github.com/marcus-qen/legator/internal/protocol"
)

const (
	kubeflowApprovalProbePrefix = "kubeflow:"
	kubeflowPayloadArgPrefix    = "--legator-kubeflow="
)

type kubeflowApprovalPayload struct {
	Version string                     `json:"version"`
	Action  string                     `json:"action"`
	Submit  *kubeflow.SubmitRunRequest `json:"submit,omitempty"`
	Cancel  *kubeflow.CancelRunRequest `json:"cancel,omitempty"`
}

func (s *Server) handleKubeflowRunStatus(w http.ResponseWriter, r *http.Request) {
	if s.kubeflowClient == nil {
		s.handleKubeflowUnavailable(w, r)
		return
	}

	request := kubeflow.RunStatusRequest{
		Kind:      strings.TrimSpace(r.URL.Query().Get("kind")),
		Name:      strings.TrimSpace(r.PathValue("name")),
		Namespace: strings.TrimSpace(r.URL.Query().Get("namespace")),
	}
	result, err := s.kubeflowClient.RunStatus(r.Context(), request)
	if err != nil {
		kubeflowWriteClientError(w, err)
		return
	}
	writeKubeflowJSON(w, http.StatusOK, map[string]any{"run": result})
}

func (s *Server) handleKubeflowSubmitRun(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Kubeflow.ActionsEnabled {
		writeJSONError(w, http.StatusForbidden, "action_disabled", "kubeflow actions are disabled by policy")
		return
	}
	if s.kubeflowClient == nil {
		s.handleKubeflowUnavailable(w, r)
		return
	}

	var request kubeflow.SubmitRunRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid submit payload")
		return
	}
	if request.Name == "" {
		request.Name = strings.TrimSpace(r.URL.Query().Get("name"))
	}
	if request.Kind == "" {
		request.Kind = strings.TrimSpace(r.URL.Query().Get("kind"))
	}
	if request.Namespace == "" {
		request.Namespace = strings.TrimSpace(r.URL.Query().Get("namespace"))
	}

	status, payload, err := s.submitKubeflowRunWithPolicy(r.Context(), request, "api")
	if err != nil {
		kubeflowWriteClientError(w, err)
		return
	}
	writeKubeflowJSON(w, status, payload)
}

func (s *Server) handleKubeflowCancelRun(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Kubeflow.ActionsEnabled {
		writeJSONError(w, http.StatusForbidden, "action_disabled", "kubeflow actions are disabled by policy")
		return
	}
	if s.kubeflowClient == nil {
		s.handleKubeflowUnavailable(w, r)
		return
	}

	request := kubeflow.CancelRunRequest{
		Kind:      strings.TrimSpace(r.URL.Query().Get("kind")),
		Name:      strings.TrimSpace(r.PathValue("name")),
		Namespace: strings.TrimSpace(r.URL.Query().Get("namespace")),
	}
	status, payload, err := s.cancelKubeflowRunWithPolicy(r.Context(), request, "api")
	if err != nil {
		kubeflowWriteClientError(w, err)
		return
	}
	writeKubeflowJSON(w, status, payload)
}

func (s *Server) submitKubeflowRunWithPolicy(ctx context.Context, request kubeflow.SubmitRunRequest, actor string) (int, map[string]any, error) {
	namespace := s.kubeflowNamespaceOrDefault(request.Namespace)
	payload, command, err := s.newKubeflowPolicyCommand("submit", namespace, request.Name, request.Kind, &kubeflowApprovalPayload{
		Version: "v1",
		Action:  "submit",
		Submit:  &request,
	})
	if err != nil {
		return 0, nil, err
	}

	status, response, err := s.evaluateKubeflowMutationPolicy(ctx, "submit", namespace, payload, command, actor, func(execCtx context.Context) (any, error) {
		return s.kubeflowClient.SubmitRun(execCtx, request)
	})
	if err != nil {
		return 0, nil, err
	}
	if status == http.StatusOK {
		status = http.StatusAccepted
	}
	return status, response, nil
}

func (s *Server) cancelKubeflowRunWithPolicy(ctx context.Context, request kubeflow.CancelRunRequest, actor string) (int, map[string]any, error) {
	namespace := s.kubeflowNamespaceOrDefault(request.Namespace)
	payload, command, err := s.newKubeflowPolicyCommand("cancel", namespace, request.Name, request.Kind, &kubeflowApprovalPayload{
		Version: "v1",
		Action:  "cancel",
		Cancel:  &request,
	})
	if err != nil {
		return 0, nil, err
	}

	return s.evaluateKubeflowMutationPolicy(ctx, "cancel", namespace, payload, command, actor, func(execCtx context.Context) (any, error) {
		return s.kubeflowClient.CancelRun(execCtx, request)
	})
}

func (s *Server) evaluateKubeflowMutationPolicy(
	ctx context.Context,
	action string,
	namespace string,
	target string,
	command protocol.CommandPayload,
	actor string,
	execute func(context.Context) (any, error),
) (int, map[string]any, error) {
	if s.approvalCore == nil {
		result, err := execute(ctx)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{
			"status": action + "_executed",
			action:   result,
		}, nil
	}

	probeID := kubeflowApprovalProbeID(namespace)
	policyResult, err := s.approvalCore.SubmitCommandApprovalWithContext(
		ctx,
		probeID,
		&command,
		kubeflowPolicyLevelForAction(action),
		fmt.Sprintf("Kubeflow %s %s", action, target),
		actor,
	)
	if err != nil {
		return 0, nil, fmt.Errorf("approval queue: %w", err)
	}

	if policyResult == nil {
		return 0, nil, fmt.Errorf("policy decision unavailable")
	}

	decision := policyResult.Decision
	response := map[string]any{
		"policy_decision":  decision.Outcome,
		"risk_level":       decision.RiskLevel,
		"policy_rationale": decision.Rationale,
	}

	switch decision.Outcome {
	case approvalpolicy.CommandPolicyDecisionDeny:
		s.emitAudit(audit.EventAuthorizationDenied, probeID, actor, fmt.Sprintf("Kubeflow %s denied by policy: %s", action, target))
		s.publishEvent(events.CommandFailed, probeID, fmt.Sprintf("Kubeflow %s denied: %s", action, target), map[string]any{"action": action, "target": target})
		response["status"] = "denied"
		response["message"] = "Kubeflow mutation denied by policy."
		return http.StatusTooManyRequests, response, nil
	case approvalpolicy.CommandPolicyDecisionQueue:
		if policyResult.Request == nil {
			return 0, nil, fmt.Errorf("approval queue unavailable")
		}
		req := policyResult.Request
		s.emitAudit(audit.EventApprovalRequest, probeID, actor, fmt.Sprintf("Kubeflow %s requires approval: %s (risk: %s)", action, target, req.RiskLevel))
		s.publishEvent(events.ApprovalNeeded, probeID, fmt.Sprintf("Kubeflow %s queued for approval", action), map[string]any{"approval_id": req.ID, "target": target, "action": action})
		response["status"] = "pending_approval"
		response["approval_id"] = req.ID
		response["expires_at"] = req.ExpiresAt
		response["message"] = "Kubeflow mutation requires human approval. Use POST /api/v1/approvals/{id}/decide to approve or deny."
		return http.StatusAccepted, response, nil
	}

	s.emitAudit(audit.EventCommandSent, probeID, actor, fmt.Sprintf("Kubeflow %s requested: %s", action, target))
	s.publishEvent(events.CommandDispatched, probeID, fmt.Sprintf("Kubeflow %s dispatched", action), map[string]any{"action": action, "target": target})

	result, err := execute(ctx)
	if err != nil {
		s.emitAudit(audit.EventCommandResult, probeID, actor, fmt.Sprintf("Kubeflow %s failed: %s (%v)", action, target, err))
		s.publishEvent(events.CommandFailed, probeID, fmt.Sprintf("Kubeflow %s failed", action), map[string]any{"action": action, "target": target, "error": err.Error()})
		return 0, nil, err
	}

	s.emitAudit(audit.EventCommandResult, probeID, actor, fmt.Sprintf("Kubeflow %s completed: %s", action, target))
	s.publishEvent(events.CommandCompleted, probeID, fmt.Sprintf("Kubeflow %s completed", action), map[string]any{"action": action, "target": target})

	response["status"] = action + "_executed"
	response[action] = result
	return http.StatusOK, response, nil
}

func (s *Server) newKubeflowPolicyCommand(action, namespace, name, kind string, payload *kubeflowApprovalPayload) (string, protocol.CommandPayload, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return "", protocol.CommandPayload{}, &kubeflow.ClientError{Code: "invalid_request", Message: "run name is required"}
	}
	trimmedKind := strings.TrimSpace(kind)
	if trimmedKind == "" {
		trimmedKind = kubeflow.DefaultRunResource
	}
	target := fmt.Sprintf("%s/%s", trimmedKind, trimmedName)

	encoded, err := encodeKubeflowPayload(payload)
	if err != nil {
		return "", protocol.CommandPayload{}, err
	}

	cmd := protocol.CommandPayload{
		RequestID: corecommanddispatch.NextCommandRequestID(),
		Command:   fmt.Sprintf("kubeflow %s %s", action, target),
		Args:      []string{kubeflowPayloadArgPrefix + encoded},
		Timeout:   s.cfg.Kubeflow.TimeoutDuration(),
		Level:     kubeflowPolicyLevelForAction(action),
	}
	return target, cmd, nil
}

func kubeflowPolicyLevelForAction(action string) protocol.CapabilityLevel {
	if strings.EqualFold(strings.TrimSpace(action), "cancel") {
		return protocol.CapRemediate
	}
	return protocol.CapDiagnose
}

func encodeKubeflowPayload(payload *kubeflowApprovalPayload) (string, error) {
	if payload == nil {
		return "", &kubeflow.ClientError{Code: "invalid_request", Message: "kubeflow payload is required"}
	}
	if strings.TrimSpace(payload.Version) == "" {
		payload.Version = "v1"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", &kubeflow.ClientError{Code: "invalid_request", Message: "failed to encode kubeflow payload", Detail: err.Error()}
	}
	return base64.RawURLEncoding.EncodeToString(body), nil
}

func decodeKubeflowPayload(cmd protocol.CommandPayload) (*kubeflowApprovalPayload, error) {
	var encoded string
	for _, arg := range cmd.Args {
		if strings.HasPrefix(arg, kubeflowPayloadArgPrefix) {
			encoded = strings.TrimPrefix(arg, kubeflowPayloadArgPrefix)
			break
		}
	}
	if encoded == "" {
		return nil, fmt.Errorf("kubeflow approval payload missing")
	}

	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode kubeflow approval payload: %w", err)
	}

	var payload kubeflowApprovalPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal kubeflow approval payload: %w", err)
	}
	return &payload, nil
}

func kubeflowApprovalProbeID(namespace string) string {
	ns := strings.TrimSpace(namespace)
	if ns == "" {
		ns = "kubeflow"
	}
	return kubeflowApprovalProbePrefix + ns
}

func (s *Server) kubeflowNamespaceOrDefault(namespace string) string {
	ns := strings.TrimSpace(namespace)
	if ns != "" {
		return ns
	}
	if s.cfg.Kubeflow.NamespaceOrDefault() != "" {
		return s.cfg.Kubeflow.NamespaceOrDefault()
	}
	return "kubeflow"
}

func writeKubeflowJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func kubeflowWriteClientError(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "unexpected kubeflow error")
		return
	}

	var clientErr *kubeflow.ClientError
	if !errors.As(err, &clientErr) {
		writeJSONError(w, http.StatusBadGateway, "kubeflow_error", err.Error())
		return
	}

	switch clientErr.Code {
	case "cli_missing":
		writeJSONError(w, http.StatusServiceUnavailable, clientErr.Code, clientErr.Message)
	case "namespace_missing", "resource_missing":
		writeJSONError(w, http.StatusNotFound, clientErr.Code, clientErr.Message)
	case "invalid_request":
		writeJSONError(w, http.StatusBadRequest, clientErr.Code, clientErr.Error())
	case "auth_failed", "cluster_unreachable", "timeout", "inventory_unavailable", "command_failed", "parse_error":
		writeJSONError(w, http.StatusBadGateway, clientErr.Code, clientErr.Error())
	default:
		writeJSONError(w, http.StatusBadGateway, "kubeflow_error", clientErr.Error())
	}
}

func (s *Server) dispatchApprovedCommand(probeID string, cmd protocol.CommandPayload) error {
	if strings.HasPrefix(strings.TrimSpace(probeID), kubeflowApprovalProbePrefix) {
		return s.dispatchApprovedKubeflowMutation(probeID, cmd)
	}
	return s.dispatchCore.Dispatch(probeID, cmd)
}

func (s *Server) dispatchApprovedKubeflowMutation(probeID string, cmd protocol.CommandPayload) error {
	if s.kubeflowClient == nil {
		return fmt.Errorf("kubeflow adapter unavailable")
	}
	if !s.cfg.Kubeflow.ActionsEnabled {
		return fmt.Errorf("kubeflow actions are disabled by policy")
	}

	payload, err := decodeKubeflowPayload(cmd)
	if err != nil {
		return err
	}
	actor := "approval"

	switch strings.ToLower(strings.TrimSpace(payload.Action)) {
	case "submit":
		if payload.Submit == nil {
			return fmt.Errorf("submit payload missing")
		}
		result, err := s.kubeflowClient.SubmitRun(context.Background(), *payload.Submit)
		if err != nil {
			s.emitAudit(audit.EventCommandResult, probeID, actor, fmt.Sprintf("Approved kubeflow submit failed: %v", err))
			s.publishEvent(events.CommandFailed, probeID, "Approved kubeflow submit failed", map[string]any{"error": err.Error()})
			return err
		}
		s.emitAudit(audit.EventCommandResult, probeID, actor, fmt.Sprintf("Approved kubeflow submit completed: %s/%s", result.Run.Kind, result.Run.Name))
		s.publishEvent(events.CommandCompleted, probeID, "Approved kubeflow submit completed", map[string]any{"kind": result.Run.Kind, "name": result.Run.Name})
		return nil
	case "cancel":
		if payload.Cancel == nil {
			return fmt.Errorf("cancel payload missing")
		}
		result, err := s.kubeflowClient.CancelRun(context.Background(), *payload.Cancel)
		if err != nil {
			s.emitAudit(audit.EventCommandResult, probeID, actor, fmt.Sprintf("Approved kubeflow cancel failed: %v", err))
			s.publishEvent(events.CommandFailed, probeID, "Approved kubeflow cancel failed", map[string]any{"error": err.Error()})
			return err
		}
		s.emitAudit(audit.EventCommandResult, probeID, actor, fmt.Sprintf("Approved kubeflow cancel completed: %s/%s", result.Run.Kind, result.Run.Name))
		s.publishEvent(events.CommandCompleted, probeID, "Approved kubeflow cancel completed", map[string]any{"kind": result.Run.Kind, "name": result.Run.Name, "canceled": result.Canceled})
		return nil
	default:
		return fmt.Errorf("unsupported kubeflow approval action %q", payload.Action)
	}
}

func (s *Server) mcpKubeflowRunStatus(ctx context.Context, request kubeflow.RunStatusRequest) (kubeflow.RunStatusResult, error) {
	if s.kubeflowClient == nil {
		return kubeflow.RunStatusResult{}, fmt.Errorf("kubeflow adapter unavailable")
	}
	return s.kubeflowClient.RunStatus(ctx, request)
}

func (s *Server) mcpKubeflowSubmitRun(ctx context.Context, request kubeflow.SubmitRunRequest) (map[string]any, error) {
	_, payload, err := s.submitKubeflowRunWithPolicy(ctx, request, "mcp")
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func (s *Server) mcpKubeflowCancelRun(ctx context.Context, request kubeflow.CancelRunRequest) (map[string]any, error) {
	_, payload, err := s.cancelKubeflowRunWithPolicy(ctx, request, "mcp")
	if err != nil {
		return nil, err
	}
	return payload, nil
}
