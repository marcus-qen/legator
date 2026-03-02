package server

import (
	"bytes"
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

var (
	errRunnerSecretFieldRejected = errors.New("runner request includes disallowed long-lived secret field")
	runnerCreateSecretFieldSet   = map[string]struct{}{
		"access_key":            {},
		"access_key_id":         {},
		"api_key":               {},
		"aws_access_key_id":     {},
		"aws_secret_access_key": {},
		"client_secret":         {},
		"credentials":           {},
		"password":              {},
		"private_key":           {},
		"provider_api_key":      {},
		"provider_secret":       {},
		"provider_token":        {},
		"secret":                {},
		"secret_access_key":     {},
		"service_account_json":  {},
		"ssh_private_key":       {},
		"token":                 {},
	}
	runnerCreateSecretFieldSuffixes = []string{
		"_token",
		"_secret",
		"_api_key",
		"_private_key",
		"_password",
		"_credentials",
		"_secret_key",
	}
)

type createRunnerRequestPayload struct {
	Label string `json:"label"`
}

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

	req, err := decodeCreateRunnerRequest(r)
	if err != nil {
		if errors.Is(err, errRunnerSecretFieldRejected) {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}

	created, err := s.runnerManager.CreateRunner(runner.CreateRequest{
		Label:     strings.TrimSpace(req.Label),
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
		Audience:  runner.Audience(strings.TrimSpace(req.Audience)),
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}

	input := runner.LifecycleRequest{
		RunnerID:  runnerID,
		RunToken:  strings.TrimSpace(req.RunToken),
		SessionID: sessionID,
	}

	var (
		updated *runner.Runner
		err     error
	)
	switch audience {
	case runner.AudienceRunnerStart:
		updated, err = s.runnerManager.StartRunner(input)
	case runner.AudienceRunnerStop:
		updated, err = s.runnerManager.StopRunner(input)
	case runner.AudienceRunnerDestroy:
		updated, err = s.runnerManager.DestroyRunner(input)
	default:
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "unsupported runner lifecycle transition")
		return
	}
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
			"state":      updated.State,
			"session_id": sessionID,
			"audience":   audience,
		},
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updated)
}

func decodeCreateRunnerRequest(r *http.Request) (createRunnerRequestPayload, error) {
	var req createRunnerRequestPayload
	if r == nil || r.Body == nil {
		return req, nil
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return req, err
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return req, nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return req, err
	}
	if field, blocked := findDisallowedRunnerSecretField(raw); blocked {
		return req, fmt.Errorf("%w: %s", errRunnerSecretFieldRejected, field)
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return req, errors.New("invalid request")
	}
	return req, nil
}

func findDisallowedRunnerSecretField(raw map[string]json.RawMessage) (string, bool) {
	for field := range raw {
		normalized := normalizeRunnerCreateField(field)
		if _, blocked := runnerCreateSecretFieldSet[normalized]; blocked {
			return field, true
		}
		for _, suffix := range runnerCreateSecretFieldSuffixes {
			if strings.HasSuffix(normalized, suffix) {
				return field, true
			}
		}
	}
	return "", false
}

func normalizeRunnerCreateField(field string) string {
	normalized := strings.ToLower(strings.TrimSpace(field))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, ".", "_")
	return normalized
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
		errors.Is(err, runner.ErrRunTokenTTLExceeded):
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, runner.ErrRunTokenInvalid):
		writeJSONError(w, http.StatusUnauthorized, "invalid_run_token", err.Error())
	case errors.Is(err, runner.ErrRunTokenExpired):
		writeJSONError(w, http.StatusUnauthorized, "expired_run_token", err.Error())
	case errors.Is(err, runner.ErrRunTokenConsumed):
		writeJSONError(w, http.StatusConflict, "run_token_consumed", err.Error())
	case errors.Is(err, runner.ErrRunTokenSessionBound), errors.Is(err, runner.ErrRunTokenScope):
		writeJSONError(w, http.StatusForbidden, "run_token_scope_rejected", err.Error())
	case errors.Is(err, runner.ErrInvalidTransition):
		writeJSONError(w, http.StatusConflict, "invalid_transition", err.Error())
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
