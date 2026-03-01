// Package reliability provides SLO scorecards, request telemetry, failure
// drill tooling, and incident management for the Legator control plane.
package reliability

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// IncidentSeverity defines the impact level.
type IncidentSeverity string

const (
	SeverityP1 IncidentSeverity = "P1"
	SeverityP2 IncidentSeverity = "P2"
	SeverityP3 IncidentSeverity = "P3"
	SeverityP4 IncidentSeverity = "P4"
)

// IncidentStatus tracks the lifecycle state.
type IncidentStatus string

const (
	StatusOpen          IncidentStatus = "open"
	StatusInvestigating IncidentStatus = "investigating"
	StatusMitigated     IncidentStatus = "mitigated"
	StatusResolved      IncidentStatus = "resolved"
	StatusClosed        IncidentStatus = "closed"
)

// TimelineEntryType classifies timeline entries.
type TimelineEntryType string

const (
	TimelineAlertFired        TimelineEntryType = "alert_fired"
	TimelineCommandSent       TimelineEntryType = "command_sent"
	TimelineApprovalRequested TimelineEntryType = "approval_requested"
	TimelineManualNote        TimelineEntryType = "manual_note"
	TimelineStateChange       TimelineEntryType = "state_change"
)

// Incident represents a service incident.
type Incident struct {
	ID             string           `json:"id"`
	Title          string           `json:"title"`
	Severity       IncidentSeverity `json:"severity"`
	Status         IncidentStatus   `json:"status"`
	AffectedProbes []string         `json:"affected_probes"`
	StartTime      time.Time        `json:"start_time"`
	EndTime        *time.Time       `json:"end_time,omitempty"`
	RootCause      string           `json:"root_cause,omitempty"`
	Resolution     string           `json:"resolution,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// TimelineEntry records a discrete event in the incident timeline.
type TimelineEntry struct {
	ID           string            `json:"id"`
	IncidentID   string            `json:"incident_id"`
	Timestamp    time.Time         `json:"timestamp"`
	Type         TimelineEntryType `json:"type"`
	Description  string            `json:"description"`
	AuditEventID string            `json:"audit_event_id,omitempty"`
}

// IncidentFilter defines query filters for listing incidents.
type IncidentFilter struct {
	Status   IncidentStatus
	Severity IncidentSeverity
	Probe    string
	From     time.Time
	To       time.Time
}

// IncidentUpdate holds fields that can be patched on an incident.
type IncidentUpdate struct {
	Status     *IncidentStatus
	Title      *string
	EndTime    *time.Time
	RootCause  *string
	Resolution *string
}

// PostmortemRecord is the top-level struct written to incident.json in the bundle.
type PostmortemRecord struct {
	Incident Incident        `json:"incident"`
	Timeline []TimelineEntry `json:"timeline"`
}

// GeneratePostmortemBundle writes a ZIP postmortem bundle to w.
// auditStreamer is called to write audit events (as JSONL) into the bundle;
// pass nil to produce an empty audit-events.jsonl entry.
func GeneratePostmortemBundle(w io.Writer, inc Incident, timeline []TimelineEntry, auditStreamer func(io.Writer) error) error {
	zw := zip.NewWriter(w)

	// 1. incident.json — full incident record with timeline
	{
		rec := PostmortemRecord{Incident: inc, Timeline: timeline}
		fw, err := zw.Create("incident.json")
		if err != nil {
			return fmt.Errorf("create incident.json: %w", err)
		}
		enc := json.NewEncoder(fw)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("write incident.json: %w", err)
		}
	}

	// 2. timeline.jsonl — timeline entries as newline-delimited JSON
	{
		fw, err := zw.Create("timeline.jsonl")
		if err != nil {
			return fmt.Errorf("create timeline.jsonl: %w", err)
		}
		enc := json.NewEncoder(fw)
		for _, entry := range timeline {
			if encErr := enc.Encode(entry); encErr != nil {
				return fmt.Errorf("write timeline.jsonl: %w", encErr)
			}
		}
	}

	// 3. audit-events.jsonl — audit events from incident window
	{
		fw, err := zw.Create("audit-events.jsonl")
		if err != nil {
			return fmt.Errorf("create audit-events.jsonl: %w", err)
		}
		if auditStreamer != nil {
			// Non-fatal: best-effort audit streaming
			_ = auditStreamer(fw)
		}
	}

	// 4. README.md — human-readable postmortem template
	{
		fw, err := zw.Create("README.md")
		if err != nil {
			return fmt.Errorf("create README.md: %w", err)
		}
		if _, err := fmt.Fprint(fw, buildPostmortemReadme(inc, timeline)); err != nil {
			return fmt.Errorf("write README.md: %w", err)
		}
	}

	return zw.Close()
}

// buildPostmortemReadme generates a human-readable postmortem template pre-filled
// with incident details.
func buildPostmortemReadme(inc Incident, timeline []TimelineEntry) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# Postmortem: %s\n\n", inc.Title))
	sb.WriteString(fmt.Sprintf("**Incident ID:** %s  \n", inc.ID))
	sb.WriteString(fmt.Sprintf("**Severity:** %s  \n", inc.Severity))
	sb.WriteString(fmt.Sprintf("**Status:** %s  \n", inc.Status))
	sb.WriteString(fmt.Sprintf("**Start Time:** %s  \n", inc.StartTime.UTC().Format(time.RFC3339)))

	if inc.EndTime != nil {
		sb.WriteString(fmt.Sprintf("**End Time:** %s  \n", inc.EndTime.UTC().Format(time.RFC3339)))
		duration := inc.EndTime.Sub(inc.StartTime)
		sb.WriteString(fmt.Sprintf("**Duration:** %s  \n", duration.Round(time.Second)))
	} else {
		sb.WriteString("**End Time:** (ongoing)  \n")
	}

	if len(inc.AffectedProbes) > 0 {
		sb.WriteString(fmt.Sprintf("**Affected Probes:** %s  \n", strings.Join(inc.AffectedProbes, ", ")))
	}

	sb.WriteString("\n## Summary\n\n")
	sb.WriteString("_Fill in the executive summary here._\n\n")

	sb.WriteString("## Root Cause\n\n")
	if inc.RootCause != "" {
		sb.WriteString(inc.RootCause + "\n\n")
	} else {
		sb.WriteString("_Root cause not yet determined._\n\n")
	}

	sb.WriteString("## Resolution\n\n")
	if inc.Resolution != "" {
		sb.WriteString(inc.Resolution + "\n\n")
	} else {
		sb.WriteString("_Resolution not yet documented._\n\n")
	}

	sb.WriteString("## Timeline\n\n")
	if len(timeline) > 0 {
		for _, e := range timeline {
			sb.WriteString(fmt.Sprintf("- **%s** [%s] %s\n",
				e.Timestamp.UTC().Format("2006-01-02 15:04:05 UTC"),
				e.Type,
				e.Description,
			))
		}
	} else {
		sb.WriteString("_No timeline entries recorded._\n")
	}

	sb.WriteString("\n## Action Items\n\n")
	sb.WriteString("| Action | Owner | Due Date |\n")
	sb.WriteString("|--------|-------|----------|\n")
	sb.WriteString("| (add action items) | | |\n\n")

	sb.WriteString("## Lessons Learned\n\n")
	sb.WriteString("_What went well? What could be improved?_\n")

	return sb.String()
}
