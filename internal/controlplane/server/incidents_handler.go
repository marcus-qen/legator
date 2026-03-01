package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/reliability"
)

// handleCreateIncident creates a new incident.
func (s *Server) handleCreateIncident(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title          string                       `json:"title"`
		Severity       reliability.IncidentSeverity `json:"severity"`
		AffectedProbes []string                     `json:"affected_probes"`
		StartTime      *time.Time                   `json:"start_time,omitempty"`
		RootCause      string                       `json:"root_cause,omitempty"`
		Resolution     string                       `json:"resolution,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if req.Title == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "title is required")
		return
	}
	if req.Severity == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "severity is required (P1-P4)")
		return
	}

	inc := reliability.Incident{
		Title:          req.Title,
		Severity:       req.Severity,
		Status:         reliability.StatusOpen,
		AffectedProbes: req.AffectedProbes,
		RootCause:      req.RootCause,
		Resolution:     req.Resolution,
	}
	if req.StartTime != nil {
		inc.StartTime = *req.StartTime
	}

	created, err := s.incidentStore.Create(inc)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(created)
}

// handleListIncidents lists incidents with optional filters.
func (s *Server) handleListIncidents(w http.ResponseWriter, r *http.Request) {
	f := reliability.IncidentFilter{}
	q := r.URL.Query()

	if v := q.Get("status"); v != "" {
		f.Status = reliability.IncidentStatus(v)
	}
	if v := q.Get("severity"); v != "" {
		f.Severity = reliability.IncidentSeverity(v)
	}
	if v := q.Get("probe"); v != "" {
		f.Probe = v
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = t
		}
	}

	incidents, err := s.incidentStore.List(f)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"incidents": incidents,
		"count":     len(incidents),
	})
}

// handleGetIncident returns a single incident with its timeline.
func (s *Server) handleGetIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing incident id")
		return
	}

	inc, found, err := s.incidentStore.Get(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeJSONError(w, http.StatusNotFound, "not_found", "incident not found")
		return
	}

	timeline, err := s.incidentStore.GetTimeline(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"incident": inc,
		"timeline": timeline,
	})
}

// handleUpdateIncident patches an incident's status, root_cause, resolution, etc.
func (s *Server) handleUpdateIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing incident id")
		return
	}

	// Verify existence before parsing body
	_, found, err := s.incidentStore.Get(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeJSONError(w, http.StatusNotFound, "not_found", "incident not found")
		return
	}

	var body struct {
		Status     *string    `json:"status,omitempty"`
		Title      *string    `json:"title,omitempty"`
		EndTime    *time.Time `json:"end_time,omitempty"`
		RootCause  *string    `json:"root_cause,omitempty"`
		Resolution *string    `json:"resolution,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	upd := reliability.IncidentUpdate{
		Title:      body.Title,
		EndTime:    body.EndTime,
		RootCause:  body.RootCause,
		Resolution: body.Resolution,
	}
	if body.Status != nil {
		s2 := reliability.IncidentStatus(*body.Status)
		upd.Status = &s2
	}

	updated, err := s.incidentStore.Update(id, upd)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updated)
}

// handleAddTimelineEntry adds a timeline entry to an incident.
func (s *Server) handleAddTimelineEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing incident id")
		return
	}

	// Verify incident exists
	_, found, err := s.incidentStore.Get(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeJSONError(w, http.StatusNotFound, "not_found", "incident not found")
		return
	}

	var body struct {
		Type         reliability.TimelineEntryType `json:"type"`
		Description  string                        `json:"description"`
		AuditEventID string                        `json:"audit_event_id,omitempty"`
		Timestamp    *time.Time                    `json:"timestamp,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if body.Type == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "type is required")
		return
	}
	if body.Description == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "description is required")
		return
	}

	entry := reliability.TimelineEntry{
		IncidentID:   id,
		Type:         body.Type,
		Description:  body.Description,
		AuditEventID: body.AuditEventID,
	}
	if body.Timestamp != nil {
		entry.Timestamp = *body.Timestamp
	}

	created, err := s.incidentStore.AddTimelineEntry(entry)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(created)
}

// handleDeleteIncident soft-deletes an incident.
func (s *Server) handleDeleteIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing incident id")
		return
	}

	// Check existence first for a proper 404
	_, found, err := s.incidentStore.Get(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeJSONError(w, http.StatusNotFound, "not_found", "incident not found")
		return
	}

	if err := s.incidentStore.SoftDelete(id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted", "id": id})
}

// handleExportIncident streams a ZIP postmortem bundle.
func (s *Server) handleExportIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing incident id")
		return
	}

	inc, found, err := s.incidentStore.Get(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeJSONError(w, http.StatusNotFound, "not_found", "incident not found")
		return
	}

	timeline, err := s.incidentStore.GetTimeline(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	auditStreamer := s.buildIncidentAuditStreamer(r.Context(), inc)

	filename := fmt.Sprintf("postmortem-%s.zip", id)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	if err := reliability.GeneratePostmortemBundle(w, inc, timeline, auditStreamer); err != nil {
		// Headers already sent; can only log
		s.logger.Sugar().Warnf("postmortem bundle generation failed for incident %s: %v", id, err)
	}
}

// buildIncidentAuditStreamer returns a function that streams audit events
// from the incident window (StartTime - 30min to EndTime + 30min).
func (s *Server) buildIncidentAuditStreamer(ctx context.Context, inc reliability.Incident) func(io.Writer) error {
	if s.auditStore == nil {
		return nil
	}
	return func(w io.Writer) error {
		startWindow := inc.StartTime.Add(-30 * time.Minute)
		var endWindow time.Time
		if inc.EndTime != nil {
			endWindow = inc.EndTime.Add(30 * time.Minute)
		} else {
			endWindow = time.Now().UTC().Add(30 * time.Minute)
		}
		f := audit.Filter{
			Since: startWindow,
			Until: endWindow,
		}
		return s.auditStore.StreamJSONL(ctx, w, f)
	}
}

// initIncidents sets up the incident store.
func (s *Server) initIncidents() {
	incidentDBPath := filepath.Join(s.cfg.DataDir, "incidents.db")
	store, err := reliability.NewIncidentStore(incidentDBPath)
	if err != nil {
		s.logger.Sugar().Warnf("cannot open incidents database, incident management disabled: %v", err)
	} else {
		s.incidentStore = store
		s.logger.Sugar().Infof("incident store opened: %s", incidentDBPath)
	}
}

// handleIncidentsUnavailable is the fallback if the incident store is not initialised.
func (s *Server) handleIncidentsUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "incident management unavailable")
}
