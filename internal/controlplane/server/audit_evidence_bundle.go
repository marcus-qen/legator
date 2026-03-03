package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/compliance"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"go.uber.org/zap"
)

const evidenceComplianceHistoryLimit = 5000

type auditEvidenceBundleFilter struct {
	Since       time.Time
	Until       time.Time
	Framework   string
	ProbeIDs    []string
	WorkspaceID string
}

type evidenceBundleFile struct {
	Name    string
	Content []byte
}

type evidenceBundleManifest struct {
	GeneratedAt string                         `json:"generated_at"`
	Filters     evidenceBundleManifestFilter   `json:"filters"`
	Files       []evidenceBundleManifestDigest `json:"files"`
}

type evidenceBundleManifestFilter struct {
	Since     string   `json:"since,omitempty"`
	Until     string   `json:"until,omitempty"`
	Framework string   `json:"framework,omitempty"`
	ProbeIDs  []string `json:"probe_ids,omitempty"`
}

type evidenceBundleManifestDigest struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

type evidenceInventorySnapshot struct {
	CapturedAt string              `json:"captured_at"`
	Inventory  fleet.FleetInventory `json:"inventory"`
}

type evidenceComplianceSnapshot struct {
	CapturedAt string                        `json:"captured_at"`
	Framework  string                        `json:"framework,omitempty"`
	Results    []compliance.ComplianceResult `json:"results"`
	Total      int                           `json:"total"`
}

type evidenceApprovalSnapshot struct {
	CapturedAt string             `json:"captured_at"`
	Approvals  []approval.Request `json:"approvals"`
	Total      int                `json:"total"`
}

func (s *Server) handleAuditEvidenceBundleExport(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermAuditRead) {
		return
	}

	filter, err := auditEvidenceBundleFilterFromRequest(r)
	if wsID := s.workspaceJobFilter(r); wsID != "" {
		filter.WorkspaceID = wsID
	}

	status := "failure"
	errorDetail := ""
	defer func() {
		s.recordAuditEvidenceBundleExport(r, filter, status, errorDetail)
	}()

	if err != nil {
		errorDetail = err.Error()
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if s.auditStore == nil {
		errorDetail = "audit export requires persistent audit store"
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", errorDetail)
		return
	}

	generatedAt := time.Now().UTC()
	scopedProbes := s.probesForRequest(r)

	bundle, manifest, err := s.buildAuditEvidenceBundle(generatedAt, filter, scopedProbes)
	if err != nil {
		errorDetail = err.Error()
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	filename := fmt.Sprintf("legator-evidence-%s.zip", generatedAt.Format("20060102T150405Z"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("X-Legator-Evidence-Manifest-Generated-At", manifest.GeneratedAt)
	if _, err := w.Write(bundle); err != nil {
		errorDetail = err.Error()
		s.logger.Warn("write audit evidence bundle failed", zap.Error(err))
		return
	}

	status = "success"
}

func auditEvidenceBundleFilterFromRequest(r *http.Request) (auditEvidenceBundleFilter, error) {
	q := r.URL.Query()
	filter := auditEvidenceBundleFilter{}

	if rawSince := strings.TrimSpace(q.Get("since")); rawSince != "" {
		since, err := parseRFC3339(rawSince)
		if err != nil {
			return filter, fmt.Errorf("invalid since timestamp")
		}
		filter.Since = since
	}
	if rawUntil := strings.TrimSpace(q.Get("until")); rawUntil != "" {
		until, err := parseRFC3339(rawUntil)
		if err != nil {
			return filter, fmt.Errorf("invalid until timestamp")
		}
		filter.Until = until
	}
	if !filter.Since.IsZero() && !filter.Until.IsZero() && filter.Until.Before(filter.Since) {
		return filter, fmt.Errorf("until must be greater than or equal to since")
	}

	filter.Framework = strings.TrimSpace(q.Get("framework"))
	filter.ProbeIDs = dedupeStrings(append(
		append(
			parseCSVValues(q.Get("probe_ids")),
			q["probe_id"]...,
		),
		append(q["probe"], q["probes"]...)...,
	))

	return filter, nil
}

func (s *Server) buildAuditEvidenceBundle(generatedAt time.Time, filter auditEvidenceBundleFilter, scopedProbes []*fleet.ProbeState) ([]byte, evidenceBundleManifest, error) {
	auditEvents, err := s.auditStore.QueryPersisted(audit.Filter{
		Since:       filter.Since,
		Until:       filter.Until,
		WorkspaceID: filter.WorkspaceID,
	})
	if err != nil {
		return nil, evidenceBundleManifest{}, fmt.Errorf("query audit events: %w", err)
	}

	probeSet := stringSet(filter.ProbeIDs)
	auditEvents = filterAuditEventsByProbeIDs(auditEvents, probeSet)

	auditJSONL, err := marshalAuditEventsJSONL(auditEvents)
	if err != nil {
		return nil, evidenceBundleManifest{}, fmt.Errorf("marshal audit jsonl: %w", err)
	}

	selectedProbes := filterProbeStatesByIDs(scopedProbes, probeSet)
	inventorySnapshot := evidenceInventorySnapshot{
		CapturedAt: generatedAt.Format(time.RFC3339Nano),
		Inventory:  buildInventoryFromProbes(selectedProbes, fleet.InventoryFilter{}),
	}
	inventoryJSON, err := json.MarshalIndent(inventorySnapshot, "", "  ")
	if err != nil {
		return nil, evidenceBundleManifest{}, fmt.Errorf("marshal inventory snapshot: %w", err)
	}
	inventoryJSON = append(inventoryJSON, '\n')

	complianceResults, err := s.collectEvidenceComplianceResults(filter, probeSet)
	if err != nil {
		return nil, evidenceBundleManifest{}, fmt.Errorf("collect compliance results: %w", err)
	}
	complianceSnapshot := evidenceComplianceSnapshot{
		CapturedAt: generatedAt.Format(time.RFC3339Nano),
		Framework:  filter.Framework,
		Results:    complianceResults,
		Total:      len(complianceResults),
	}
	complianceJSON, err := json.MarshalIndent(complianceSnapshot, "", "  ")
	if err != nil {
		return nil, evidenceBundleManifest{}, fmt.Errorf("marshal compliance snapshot: %w", err)
	}
	complianceJSON = append(complianceJSON, '\n')

	changeDiffEvents := filterChangeDiffEvents(auditEvents)
	changeDiffJSONL, err := marshalAuditEventsJSONL(changeDiffEvents)
	if err != nil {
		return nil, evidenceBundleManifest{}, fmt.Errorf("marshal change diff jsonl: %w", err)
	}

	approvalRecords := s.collectEvidenceApprovalRecords(filter, probeSet)
	approvalSnapshot := evidenceApprovalSnapshot{
		CapturedAt: generatedAt.Format(time.RFC3339Nano),
		Approvals:  approvalRecords,
		Total:      len(approvalRecords),
	}
	approvalJSON, err := json.MarshalIndent(approvalSnapshot, "", "  ")
	if err != nil {
		return nil, evidenceBundleManifest{}, fmt.Errorf("marshal approval snapshot: %w", err)
	}
	approvalJSON = append(approvalJSON, '\n')

	files := []evidenceBundleFile{
		{Name: "audit-log.jsonl", Content: auditJSONL},
		{Name: "inventory-snapshots.json", Content: inventoryJSON},
		{Name: "compliance-check-results.json", Content: complianceJSON},
		{Name: "change-diffs.jsonl", Content: changeDiffJSONL},
		{Name: "approval-records.json", Content: approvalJSON},
	}

	manifest := buildEvidenceBundleManifest(generatedAt, filter, files)
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, evidenceBundleManifest{}, fmt.Errorf("marshal manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')
	files = append(files, evidenceBundleFile{Name: "manifest.json", Content: manifestBytes})

	bundle, err := zipEvidenceFiles(files)
	if err != nil {
		return nil, evidenceBundleManifest{}, err
	}

	return bundle, manifest, nil
}

func (s *Server) collectEvidenceComplianceResults(filter auditEvidenceBundleFilter, probeSet map[string]struct{}) ([]compliance.ComplianceResult, error) {
	if s.complianceStore == nil {
		return []compliance.ComplianceResult{}, nil
	}

	results, err := s.complianceStore.History("", "", evidenceComplianceHistoryLimit)
	if err != nil {
		return nil, err
	}

	framework := strings.ToLower(strings.TrimSpace(filter.Framework))
	out := make([]compliance.ComplianceResult, 0, len(results))
	for _, result := range results {
		if len(probeSet) > 0 {
			if _, ok := probeSet[result.ProbeID]; !ok {
				continue
			}
		}
		if framework != "" && strings.ToLower(strings.TrimSpace(result.Category)) != framework {
			continue
		}
		if !filter.Since.IsZero() && result.Timestamp.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && result.Timestamp.After(filter.Until) {
			continue
		}
		out = append(out, result)
	}

	return out, nil
}

func (s *Server) collectEvidenceApprovalRecords(filter auditEvidenceBundleFilter, probeSet map[string]struct{}) []approval.Request {
	if s.approvalQueue == nil {
		return []approval.Request{}
	}

	records := s.approvalQueue.AllByWorkspace(filter.WorkspaceID, 0)
	out := make([]approval.Request, 0, len(records))
	for _, record := range records {
		if record == nil {
			continue
		}
		if len(probeSet) > 0 {
			if _, ok := probeSet[record.ProbeID]; !ok {
				continue
			}
		}
		if !timeRangeOverlaps(filter.Since, filter.Until, record.CreatedAt, approvalEndTime(record)) {
			continue
		}
		out = append(out, *record)
	}
	return out
}

func approvalEndTime(req *approval.Request) time.Time {
	if req == nil {
		return time.Time{}
	}
	if !req.DecidedAt.IsZero() {
		return req.DecidedAt
	}
	if !req.ExpiresAt.IsZero() {
		return req.ExpiresAt
	}
	return req.CreatedAt
}

func timeRangeOverlaps(since, until, start, end time.Time) bool {
	if end.IsZero() {
		end = start
	}
	if !since.IsZero() && end.Before(since) {
		return false
	}
	if !until.IsZero() && start.After(until) {
		return false
	}
	return true
}

func buildEvidenceBundleManifest(generatedAt time.Time, filter auditEvidenceBundleFilter, files []evidenceBundleFile) evidenceBundleManifest {
	manifest := evidenceBundleManifest{
		GeneratedAt: generatedAt.Format(time.RFC3339Nano),
		Filters: evidenceBundleManifestFilter{
			Since:     formatRFC3339(filter.Since),
			Until:     formatRFC3339(filter.Until),
			Framework: filter.Framework,
			ProbeIDs:  append([]string(nil), filter.ProbeIDs...),
		},
		Files: make([]evidenceBundleManifestDigest, 0, len(files)),
	}

	for _, file := range files {
		sum := sha256.Sum256(file.Content)
		manifest.Files = append(manifest.Files, evidenceBundleManifestDigest{
			Name:   file.Name,
			SHA256: hex.EncodeToString(sum[:]),
			Bytes:  len(file.Content),
		})
	}

	sort.Slice(manifest.Files, func(i, j int) bool {
		return manifest.Files[i].Name < manifest.Files[j].Name
	})
	return manifest
}

func zipEvidenceFiles(files []evidenceBundleFile) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, file := range files {
		w, err := zw.Create(file.Name)
		if err != nil {
			_ = zw.Close()
			return nil, fmt.Errorf("create zip entry %s: %w", file.Name, err)
		}
		if _, err := w.Write(file.Content); err != nil {
			_ = zw.Close()
			return nil, fmt.Errorf("write zip entry %s: %w", file.Name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}
	return buf.Bytes(), nil
}

func filterProbeStatesByIDs(probes []*fleet.ProbeState, probeSet map[string]struct{}) []*fleet.ProbeState {
	if len(probeSet) == 0 {
		return probes
	}
	filtered := make([]*fleet.ProbeState, 0, len(probes))
	for _, probe := range probes {
		if probe == nil {
			continue
		}
		if _, ok := probeSet[probe.ID]; ok {
			filtered = append(filtered, probe)
		}
	}
	return filtered
}

func filterAuditEventsByProbeIDs(events []audit.Event, probeSet map[string]struct{}) []audit.Event {
	if len(probeSet) == 0 {
		return events
	}
	out := make([]audit.Event, 0, len(events))
	for _, evt := range events {
		if _, ok := probeSet[evt.ProbeID]; ok {
			out = append(out, evt)
		}
	}
	return out
}

func filterChangeDiffEvents(events []audit.Event) []audit.Event {
	out := make([]audit.Event, 0, len(events))
	for _, evt := range events {
		if evt.Before != nil || evt.After != nil {
			out = append(out, evt)
		}
	}
	return out
}

func marshalAuditEventsJSONL(events []audit.Event) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, event := range events {
		if err := enc.Encode(event); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func formatRFC3339(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339Nano)
}

func parseCSVValues(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func stringSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func (s *Server) recordAuditEvidenceBundleExport(r *http.Request, filter auditEvidenceBundleFilter, status, errDetail string) {
	if s == nil || r == nil {
		return
	}

	summary := "Audit evidence bundle export succeeded"
	if status != "success" {
		summary = "Audit evidence bundle export failed"
	}

	detail := map[string]any{
		"status":    status,
		"path":      r.URL.Path,
		"method":    r.Method,
		"since":     formatRFC3339(filter.Since),
		"until":     formatRFC3339(filter.Until),
		"framework": filter.Framework,
		"probe_ids": append([]string(nil), filter.ProbeIDs...),
	}
	if filter.WorkspaceID != "" {
		detail["workspace_id"] = filter.WorkspaceID
	}
	if strings.TrimSpace(errDetail) != "" {
		detail["error"] = errDetail
	}

	s.recordAudit(audit.Event{
		Timestamp:   time.Now().UTC(),
		Type:        audit.EventAuditEvidenceBundleExport,
		Actor:       actorFromAuthContext(r.Context()),
		Summary:     summary,
		WorkspaceID: filter.WorkspaceID,
		Detail:      detail,
	})
}
