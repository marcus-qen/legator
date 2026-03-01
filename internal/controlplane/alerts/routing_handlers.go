package alerts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// HandleListRoutingPolicies serves GET /api/v1/alerts/routing/policies.
func (rs *RoutingStore) HandleListRoutingPolicies(w http.ResponseWriter, r *http.Request) {
	policies, err := rs.ListRoutingPolicies()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"policies": policies,
		"count":    len(policies),
	})
}

// HandleCreateRoutingPolicy serves POST /api/v1/alerts/routing/policies.
func (rs *RoutingStore) HandleCreateRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	var req RoutingPolicy
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	req.ID = ""
	if err := validateRoutingPolicy(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_routing_policy", err.Error())
		return
	}
	created, err := rs.CreateRoutingPolicy(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// HandleGetRoutingPolicy serves GET /api/v1/alerts/routing/policies/{id}.
func (rs *RoutingStore) HandleGetRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing policy id")
		return
	}
	p, err := rs.GetRoutingPolicy(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "routing policy not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// HandleUpdateRoutingPolicy serves PUT /api/v1/alerts/routing/policies/{id}.
func (rs *RoutingStore) HandleUpdateRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing policy id")
		return
	}
	if _, err := rs.GetRoutingPolicy(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "routing policy not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	var req RoutingPolicy
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	req.ID = id
	if err := validateRoutingPolicy(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_routing_policy", err.Error())
		return
	}
	updated, err := rs.UpdateRoutingPolicy(req)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "routing policy not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// HandleDeleteRoutingPolicy serves DELETE /api/v1/alerts/routing/policies/{id}.
func (rs *RoutingStore) HandleDeleteRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing policy id")
		return
	}
	if err := rs.DeleteRoutingPolicy(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "routing policy not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleResolveRouting serves POST /api/v1/alerts/routing/resolve.
// Body: RoutingContext JSON. Returns RoutingOutcome with explainability fields.
func (rs *RoutingStore) HandleResolveRouting(w http.ResponseWriter, r *http.Request) {
	var ctx RoutingContext
	if err := json.NewDecoder(r.Body).Decode(&ctx); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	outcome, err := rs.Resolve(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, outcome)
}

// -------------------------------------------------------------------
// Escalation policy handlers
// -------------------------------------------------------------------

// HandleListEscalationPolicies serves GET /api/v1/alerts/escalation/policies.
func (rs *RoutingStore) HandleListEscalationPolicies(w http.ResponseWriter, r *http.Request) {
	eps, err := rs.ListEscalationPolicies()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"policies": eps,
		"count":    len(eps),
	})
}

// HandleCreateEscalationPolicy serves POST /api/v1/alerts/escalation/policies.
func (rs *RoutingStore) HandleCreateEscalationPolicy(w http.ResponseWriter, r *http.Request) {
	var req EscalationPolicy
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	req.ID = ""
	if err := validateEscalationPolicy(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_escalation_policy", err.Error())
		return
	}
	created, err := rs.CreateEscalationPolicy(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// HandleGetEscalationPolicy serves GET /api/v1/alerts/escalation/policies/{id}.
func (rs *RoutingStore) HandleGetEscalationPolicy(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing policy id")
		return
	}
	ep, err := rs.GetEscalationPolicy(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "escalation policy not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ep)
}

// HandleUpdateEscalationPolicy serves PUT /api/v1/alerts/escalation/policies/{id}.
func (rs *RoutingStore) HandleUpdateEscalationPolicy(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing policy id")
		return
	}
	if _, err := rs.GetEscalationPolicy(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "escalation policy not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	var req EscalationPolicy
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	req.ID = id
	if err := validateEscalationPolicy(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_escalation_policy", err.Error())
		return
	}
	updated, err := rs.UpdateEscalationPolicy(req)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "escalation policy not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// HandleDeleteEscalationPolicy serves DELETE /api/v1/alerts/escalation/policies/{id}.
func (rs *RoutingStore) HandleDeleteEscalationPolicy(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing policy id")
		return
	}
	if err := rs.DeleteEscalationPolicy(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "escalation policy not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -------------------------------------------------------------------
// Validation helpers
// -------------------------------------------------------------------

func validateRoutingPolicy(p RoutingPolicy) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(p.OwnerLabel) == "" {
		return fmt.Errorf("owner_label is required")
	}
	for _, m := range p.Matchers {
		if err := validateRoutingMatcher(m); err != nil {
			return err
		}
	}
	return nil
}

func validateRoutingMatcher(m RoutingMatcher) error {
	switch strings.ToLower(strings.TrimSpace(m.Field)) {
	case "severity", "condition_type", "rule_name", "tag":
	default:
		return fmt.Errorf("unsupported matcher field %q; must be severity, condition_type, rule_name, or tag", m.Field)
	}
	switch strings.ToLower(strings.TrimSpace(m.Op)) {
	case "", "eq", "contains", "prefix":
	default:
		return fmt.Errorf("unsupported matcher op %q; must be eq, contains, or prefix", m.Op)
	}
	if strings.TrimSpace(m.Value) == "" {
		return fmt.Errorf("matcher value is required")
	}
	return nil
}

func validateEscalationPolicy(ep EscalationPolicy) error {
	if strings.TrimSpace(ep.Name) == "" {
		return fmt.Errorf("name is required")
	}
	for _, step := range ep.Steps {
		if strings.TrimSpace(step.Target) == "" {
			return fmt.Errorf("step target is required")
		}
		if strings.TrimSpace(step.TargetType) == "" {
			return fmt.Errorf("step target_type is required")
		}
		if step.Order <= 0 {
			return fmt.Errorf("step order must be >= 1")
		}
	}
	return nil
}
