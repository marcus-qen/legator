package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/runner"
)

func (s *Server) handleCreateRunner(w http.ResponseWriter, r *http.Request) {
	if s.runnerManager == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "runner manager unavailable")
		return
	}
	sessionID, actor, ok := runnerSessionContext(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "session_required", "session context required")
		return
	}

	var req struct {
		Label   string `json:"label"`
		JobID   string `json:"job_id"`
		Backend string `json:"backend"`
		Sandbox *struct {
			Image          string   `json:"image"`
			Command        []string `json:"command"`
			TimeoutSeconds int64    `json:"timeout_seconds"`
		} `json:"sandbox"`
	}
	if err := decodeOptionalJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}

	backend := runner.BackendKind(strings.TrimSpace(req.Backend))
	var sandbox *runner.SandboxContract
	if backend == runner.BackendSandbox {
		sandbox = &runner.SandboxContract{}
		if req.Sandbox != nil {
			sandbox.Image = strings.TrimSpace(req.Sandbox.Image)
			sandbox.Command = append([]string(nil), req.Sandbox.Command...)
			sandbox.TimeoutSeconds = req.Sandbox.TimeoutSeconds
		}
		if sandbox.Image == "" {
			sandbox.Image = strings.TrimSpace(s.cfg.Jobs.RunnerSandboxImage)
		}
		if sandbox.TimeoutSeconds <= 0 {
			timeout := s.cfg.Jobs.RunnerSandboxTimeoutDuration()
			if timeout > 0 {
				sandbox.TimeoutSeconds = int64(timeout / time.Second)
			}
		}
	}

	created, err := s.runnerManager.CreateRunner(runner.CreateRequest{
		Label:     strings.TrimSpace(req.Label),
		JobID:     strings.TrimSpace(req.JobID),
		Backend:   backend,
		Sandbox:   sandbox,
		CreatedBy: actor,
		SessionID: sessionID,
	})
	if err != nil {
		s.writeRunnerError(w, err)
		return
	}

	s.recordAudit(audit.Event{
		Type:    audit.EventRunnerCreated,
		Actor:   actor,
		Summary: fmt.Sprintf("Runner created: %s", created.ID),
		Detail: map[string]any{
			"runner_id":  created.ID,
			"job_id":     created.JobID,
			"backend":    created.Backend,
			"state":      created.State,
			"session_id": sessionID,
			"label":      created.Label,
		},
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(created)
}

func (s *Server) handleIssueRunToken(w http.ResponseWriter, r *http.Request) {
	if s.runnerManager == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "runner manager unavailable")
		return
	}
	sessionID, actor, ok := runnerSessionContext(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "session_required", "session context required")
		return
	}

	var req struct {
		RunnerID   string `json:"runner_id"`
		JobID      string `json:"job_id"`
		Audience   string `json:"audience"`
		TTLSeconds int64  `json:"ttl_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}

	ttl := time.Duration(req.TTLSeconds) * time.Second
	issued, err := s.runnerManager.IssueRunToken(runner.IssueTokenRequest{
		RunnerID:  strings.TrimSpace(req.RunnerID),
		JobID:     strings.TrimSpace(req.JobID),
		Audience:  runner.Audience(strings.TrimSpace(req.Audience)),
		Issuer:    actor,
		SessionID: sessionID,
		TTL:       ttl,
	})
	if err != nil {
		s.writeRunnerError(w, err)
		return
	}

	s.recordAudit(audit.Event{
		Type:    audit.EventRunnerRunTokenIssued,
		Actor:   actor,
		Summary: fmt.Sprintf("Runner run token issued: %s", issued.RunnerID),
		Detail: map[string]any{
			"runner_id":  issued.RunnerID,
			"job_id":     issued.JobID,
			"audience":   issued.Audience,
			"expires_at": issued.ExpiresAt,
			"session_id": sessionID,
		},
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(issued)
}

func (s *Server) handleStartRunner(w http.ResponseWriter, r *http.Request) {
	s.handleRunnerLifecycle(w, r, runner.AudienceRunnerStart, audit.EventRunnerStarted)
}

func (s *Server) handleStopRunner(w http.ResponseWriter, r *http.Request) {
	s.handleRunnerLifecycle(w, r, runner.AudienceRunnerStop, audit.EventRunnerStopped)
}

func (s *Server) handleDestroyRunner(w http.ResponseWriter, r *http.Request) {
	s.handleRunnerLifecycle(w, r, runner.AudienceRunnerDestroy, audit.EventRunnerDestroyed)
}

func (s *Server) handleRunnerLifecycle(w http.ResponseWriter, r *http.Request, audience runner.Audience, eventType audit.EventType) {
	if s.runnerManager == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "runner manager unavailable")
		return
	}
	runnerID := strings.TrimSpace(r.PathValue("id"))
	if runnerID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "runner id required")
		return
	}

	sessionID, actor, ok := runnerSessionContext(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "session_required", "session context required")
		return
	}

	var req struct {
		RunToken string `json:"run_token"`
		JobID    string `json:"job_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}

	input := runner.LifecycleRequest{
		RunnerID:  runnerID,
		JobID:     strings.TrimSpace(req.JobID),
		RunToken:  strings.TrimSpace(req.RunToken),
		SessionID: sessionID,
	}

	prepared, err := s.runnerManager.PrepareRunnerLifecycle(input, audience)
	if err != nil {
		s.writeRunnerError(w, err)
		return
	}

	if err := s.executeRunnerLifecycle(r.Context(), audience, prepared); err != nil {
		s.recordAudit(audit.Event{
			Type:    audit.EventRunnerError,
			Actor:   actor,
			Summary: fmt.Sprintf("Runner lifecycle backend error: %s", prepared.ID),
			Detail: map[string]any{
				"runner_id":  prepared.ID,
				"job_id":     prepared.JobID,
				"backend":    prepared.Backend,
				"audience":   audience,
				"session_id": sessionID,
				"error":      err.Error(),
			},
		})
		s.writeRunnerError(w, err)
		return
	}

	target, ok := audienceTargetState(audience)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "unsupported runner lifecycle transition")
		return
	}
	updated, err := s.runnerManager.CompleteRunnerLifecycle(prepared.ID, target)
	if err != nil {
		s.writeRunnerError(w, err)
		return
	}

	s.recordAudit(audit.Event{
		Type:    eventType,
		Actor:   actor,
		Summary: fmt.Sprintf("Runner %s: %s", audience, updated.ID),
		Detail: map[string]any{
			"runner_id":  updated.ID,
			"job_id":     updated.JobID,
			"backend":    updated.Backend,
			"state":      updated.State,
			"session_id": sessionID,
			"audience":   audience,
		},
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updated)
}

func (s *Server) executeRunnerLifecycle(ctx context.Context, audience runner.Audience, rr *runner.Runner) error {
	if rr == nil {
		return nil
	}
	if rr.Backend != runner.BackendSandbox {
		return nil
	}
	if s.runnerExecutionBackend == nil {
		return runner.ErrBackendUnavailable
	}

	timeout := time.Duration(0)
	if rr.Sandbox != nil && rr.Sandbox.TimeoutSeconds > 0 {
		timeout = time.Duration(rr.Sandbox.TimeoutSeconds) * time.Second
	}

	switch audience {
	case runner.AudienceRunnerStart:
		if rr.Sandbox == nil {
			return runner.ErrSandboxContractMalformed
		}
		if _, err := s.runnerExecutionBackend.Start(ctx, runner.StartExecutionRequest{
			RunnerID:  rr.ID,
			JobID:     rr.JobID,
			SessionID: rr.SessionID,
			Image:     rr.Sandbox.Image,
			Command:   append([]string(nil), rr.Sandbox.Command...),
			Timeout:   timeout,
		}); err != nil {
			return fmt.Errorf("%w: %v", runner.ErrBackendStartFailed, err)
		}
		return nil
	case runner.AudienceRunnerStop:
		return s.stopAndTeardownRunnerExecution(ctx, rr, "stop")
	case runner.AudienceRunnerDestroy:
		if rr.State == runner.StateRunning {
			if err := s.stopAndTeardownRunnerExecution(ctx, rr, "destroy_running"); err != nil {
				return err
			}
			return nil
		}
		if err := s.runnerExecutionBackend.Teardown(ctx, runner.TeardownExecutionRequest{
			RunnerID: rr.ID,
			Reason:   "destroy",
		}); err != nil {
			return fmt.Errorf("%w: %v", runner.ErrBackendTeardownFailed, err)
		}
		s.recordAudit(audit.Event{
			Type:    audit.EventRunnerTeardown,
			Actor:   "system",
			Summary: fmt.Sprintf("Runner teardown: %s", rr.ID),
			Detail: map[string]any{
				"runner_id": rr.ID,
				"job_id":    rr.JobID,
				"backend":   rr.Backend,
				"reason":    "destroy",
			},
		})
		return nil
	default:
		return nil
	}
}

func (s *Server) stopAndTeardownRunnerExecution(ctx context.Context, rr *runner.Runner, reason string) error {
	if rr == nil {
		return nil
	}
	if rr.Backend != runner.BackendSandbox {
		return nil
	}
	if s.runnerExecutionBackend == nil {
		return runner.ErrBackendUnavailable
	}

	var errs []error
	if err := s.runnerExecutionBackend.Stop(ctx, runner.StopExecutionRequest{RunnerID: rr.ID, Reason: reason}); err != nil {
		errs = append(errs, fmt.Errorf("%w: %v", runner.ErrBackendStopFailed, err))
	}
	if err := s.runnerExecutionBackend.Teardown(ctx, runner.TeardownExecutionRequest{RunnerID: rr.ID, Reason: reason}); err != nil {
		errs = append(errs, fmt.Errorf("%w: %v", runner.ErrBackendTeardownFailed, err))
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	s.recordAudit(audit.Event{
		Type:    audit.EventRunnerTeardown,
		Actor:   "system",
		Summary: fmt.Sprintf("Runner teardown: %s", rr.ID),
		Detail: map[string]any{
			"runner_id": rr.ID,
			"job_id":    rr.JobID,
			"backend":   rr.Backend,
			"reason":    reason,
		},
	})
	return nil
}

func (s *Server) recordRunnerBackendEvent(evt runner.BackendEvent) {
	detail := map[string]any{
		"runner_id":      strings.TrimSpace(evt.RunnerID),
		"job_id":         strings.TrimSpace(evt.JobID),
		"container_id":   strings.TrimSpace(evt.ContainerID),
		"container_name": strings.TrimSpace(evt.ContainerName),
		"reason":         strings.TrimSpace(evt.Reason),
		"event":          evt.Type,
	}
	if evt.Err != nil {
		detail["error"] = evt.Err.Error()
	}

	switch evt.Type {
	case runner.BackendEventTeardown:
		s.recordAudit(audit.Event{
			Type:    audit.EventRunnerTeardown,
			Actor:   "system",
			Summary: fmt.Sprintf("Runner teardown: %s", strings.TrimSpace(evt.RunnerID)),
			Detail:  detail,
		})
	case runner.BackendEventCommandError, runner.BackendEventTimeout:
		s.recordAudit(audit.Event{
			Type:    audit.EventRunnerError,
			Actor:   "system",
			Summary: fmt.Sprintf("Runner execution error: %s", strings.TrimSpace(evt.RunnerID)),
			Detail:  detail,
		})
	}
}

func audienceTargetState(audience runner.Audience) (runner.State, bool) {
	switch audience {
	case runner.AudienceRunnerStart:
		return runner.StateRunning, true
	case runner.AudienceRunnerStop:
		return runner.StateStopped, true
	case runner.AudienceRunnerDestroy:
		return runner.StateDestroyed, true
	default:
		return "", false
	}
}

func runnerSessionContext(r *http.Request) (sessionID, actor string, ok bool) {
	if r == nil {
		return "", "", false
	}
	user := auth.UserFromContext(r.Context())
	if user == nil {
		return "", "", false
	}
	sessionID = strings.TrimSpace(user.SessionID)
	if sessionID == "" {
		return "", "", false
	}
	actor = strings.TrimSpace(user.Username)
	if actor == "" {
		actor = strings.TrimSpace(user.ID)
	}
	if actor == "" {
		actor = "session"
	}
	return sessionID, actor, true
}

func (s *Server) writeRunnerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runner.ErrSessionRequired):
		writeJSONError(w, http.StatusUnauthorized, "session_required", err.Error())
	case errors.Is(err, runner.ErrRunnerNotFound):
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, runner.ErrRunnerIDRequired),
		errors.Is(err, runner.ErrAudienceRequired),
		errors.Is(err, runner.ErrInvalidAudience),
		errors.Is(err, runner.ErrRunTokenRequired),
		errors.Is(err, runner.ErrInvalidBackend),
		errors.Is(err, runner.ErrSandboxCommandRequired),
		errors.Is(err, runner.ErrSandboxContractMalformed):
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, runner.ErrRunTokenInvalid):
		writeJSONError(w, http.StatusUnauthorized, "invalid_run_token", err.Error())
	case errors.Is(err, runner.ErrRunTokenExpired):
		writeJSONError(w, http.StatusUnauthorized, "expired_run_token", err.Error())
	case errors.Is(err, runner.ErrRunTokenRevoked):
		writeJSONError(w, http.StatusUnauthorized, "revoked_run_token", err.Error())
	case errors.Is(err, runner.ErrRunTokenConsumed):
		writeJSONError(w, http.StatusConflict, "run_token_consumed", err.Error())
	case errors.Is(err, runner.ErrRunTokenSessionBound), errors.Is(err, runner.ErrRunTokenScope):
		writeJSONError(w, http.StatusForbidden, "run_token_scope_rejected", err.Error())
	case errors.Is(err, runner.ErrInvalidTransition):
		writeJSONError(w, http.StatusConflict, "invalid_transition", err.Error())
	case errors.Is(err, runner.ErrBackendUnavailable):
		writeJSONError(w, http.StatusServiceUnavailable, "runner_backend_unavailable", err.Error())
	case errors.Is(err, runner.ErrBackendStartFailed),
		errors.Is(err, runner.ErrBackendStopFailed),
		errors.Is(err, runner.ErrBackendTeardownFailed):
		writeJSONError(w, http.StatusBadGateway, "runner_backend_error", err.Error())
	default:
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func decodeOptionalJSONBody(r *http.Request, v any) error {
	if r == nil || r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}
