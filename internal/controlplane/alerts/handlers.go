package alerts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// HandleListRules serves GET /api/v1/alerts.
func (e *Engine) HandleListRules(w http.ResponseWriter, r *http.Request) {
	rules, err := e.store.ListRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// HandleCreateRule serves POST /api/v1/alerts.
func (e *Engine) HandleCreateRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Enabled     bool           `json:"enabled"`
		Condition   AlertCondition `json:"condition"`
		Actions     []AlertAction  `json:"actions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	rule := AlertRule{
		ID:          uuid.NewString(),
		Name:        strings.TrimSpace(req.Name),
		Description: strings.TrimSpace(req.Description),
		Enabled:     req.Enabled,
		Condition:   req.Condition,
		Actions:     req.Actions,
	}

	if err := e.validateRule(rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_rule", err.Error())
		return
	}

	created, err := e.store.CreateRule(rule)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, created)
}

// HandleGetRule serves GET /api/v1/alerts/{id}.
func (e *Engine) HandleGetRule(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing rule id")
		return
	}

	rule, err := e.store.GetRule(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, rule)
}

// HandleUpdateRule serves PUT /api/v1/alerts/{id}.
func (e *Engine) HandleUpdateRule(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing rule id")
		return
	}

	existing, err := e.store.GetRule(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	var req struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Enabled     bool           `json:"enabled"`
		Condition   AlertCondition `json:"condition"`
		Actions     []AlertAction  `json:"actions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	rule := AlertRule{
		ID:          id,
		Name:        strings.TrimSpace(req.Name),
		Description: strings.TrimSpace(req.Description),
		Enabled:     req.Enabled,
		Condition:   req.Condition,
		Actions:     req.Actions,
		CreatedAt:   existing.CreatedAt,
		UpdatedAt:   time.Now().UTC(),
	}
	if err := e.validateRule(rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_rule", err.Error())
		return
	}

	updated, err := e.store.UpdateRule(rule)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

// HandleDeleteRule serves DELETE /api/v1/alerts/{id}.
func (e *Engine) HandleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing rule id")
		return
	}

	if err := e.store.DeleteRule(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	e.evalMu.Lock()
	for key := range e.firing {
		if key.RuleID == id {
			delete(e.firing, key)
		}
	}
	for key := range e.pending {
		if key.RuleID == id {
			delete(e.pending, key)
		}
	}
	e.evalMu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

// HandleRuleHistory serves GET /api/v1/alerts/{id}/history.
func (e *Engine) HandleRuleHistory(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing rule id")
		return
	}

	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be a positive integer")
			return
		}
		limit = parsed
	}

	if _, err := e.store.GetRule(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	events := e.store.ListEvents(id, limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"rule_id": id,
		"events":  events,
		"count":   len(events),
	})
}

// HandleActiveAlerts serves GET /api/v1/alerts/active.
func (e *Engine) HandleActiveAlerts(w http.ResponseWriter, r *http.Request) {
	active := e.store.ActiveAlerts()
	writeJSON(w, http.StatusOK, map[string]any{
		"alerts": active,
		"count":  len(active),
	})
}

func (e *Engine) validateRule(rule AlertRule) error {
	if strings.TrimSpace(rule.Name) == "" {
		return fmt.Errorf("name is required")
	}

	switch rule.Condition.Type {
	case "probe_offline", "disk_threshold", "cpu_threshold":
	default:
		return fmt.Errorf("unsupported condition type: %s", rule.Condition.Type)
	}

	if _, err := parseRuleDuration(rule.Condition.Duration); err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}

	if rule.Condition.Type == "disk_threshold" || rule.Condition.Type == "cpu_threshold" {
		if rule.Condition.Threshold <= 0 || rule.Condition.Threshold > 1000 {
			return fmt.Errorf("threshold must be > 0")
		}
	}

	if len(rule.Actions) == 0 {
		return nil
	}

	webhooks := map[string]struct{}{}
	if e.notifier != nil {
		for _, cfg := range e.notifier.List() {
			webhooks[cfg.ID] = struct{}{}
		}
	}

	for _, action := range rule.Actions {
		if action.Type != "webhook" {
			return fmt.Errorf("unsupported action type: %s", action.Type)
		}
		if strings.TrimSpace(action.WebhookID) == "" {
			return fmt.Errorf("webhook_id is required")
		}
		if len(webhooks) > 0 {
			if _, ok := webhooks[action.WebhookID]; !ok {
				return fmt.Errorf("unknown webhook id: %s", action.WebhookID)
			}
		}
	}

	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{
		"error":   code,
		"message": message,
	})
}
