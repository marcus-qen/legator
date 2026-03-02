// Package compliance implements fleet-wide compliance checking.
package compliance

import (
	"context"
	"time"
)

// Status values for compliance results.
const (
	StatusPass    = "pass"
	StatusFail    = "fail"
	StatusWarning = "warning"
	StatusUnknown = "unknown"
	StatusSkipped = "skipped"
)

// Severity values for compliance checks.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
	SeverityInfo     = "info"
)

// ProbeExecutor runs a shell command against a probe and returns the output.
// exitCode -1 signals an execution error.
type ProbeExecutor func(ctx context.Context, cmd string) (output string, exitCode int, err error)

// CheckFunc is a function that evaluates one compliance check using a probe executor.
// It returns a status constant, human-readable evidence, and any execution error.
type CheckFunc func(ctx context.Context, exec ProbeExecutor) (status, evidence string, err error)

// ComplianceCheck defines a single compliance check.
type ComplianceCheck struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Category    string    `json:"category"`
	Description string    `json:"description"`
	Severity    string    `json:"severity"`
	CheckFunc   CheckFunc `json:"-"`
}

// ComplianceResult is the outcome of running one check against one probe.
type ComplianceResult struct {
	ID        string    `json:"id"`
	CheckID   string    `json:"check_id"`
	CheckName string    `json:"check_name"`
	Category  string    `json:"category"`
	Severity  string    `json:"severity"`
	ProbeID   string    `json:"probe_id"`
	Status    string    `json:"status"` // pass/fail/warning/unknown/skipped
	Evidence  string    `json:"evidence"`
	Timestamp time.Time `json:"timestamp"`
}

// CategorySummary aggregates results for one category.
type CategorySummary struct {
	Category string  `json:"category"`
	Passing  int     `json:"passing"`
	Failing  int     `json:"failing"`
	Warning  int     `json:"warning"`
	Unknown  int     `json:"unknown"`
	Total    int     `json:"total"`
	ScorePct float64 `json:"score_pct"`
}

// ComplianceSummary is the fleet-wide compliance picture.
type ComplianceSummary struct {
	TotalChecks int                        `json:"total_checks"`
	TotalProbes int                        `json:"total_probes"`
	Passing     int                        `json:"passing"`
	Failing     int                        `json:"failing"`
	Warning     int                        `json:"warning"`
	Unknown     int                        `json:"unknown"`
	ScorePct    float64                    `json:"score_pct"`
	ByCategory  map[string]CategorySummary `json:"by_category"`
}

// ScanRequest describes what to scan.
type ScanRequest struct {
	ProbeIDs []string `json:"probe_ids,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	CheckIDs []string `json:"check_ids,omitempty"`
}

// ScanResponse is returned from a POST /scan call.
type ScanResponse struct {
	ScanID    string             `json:"scan_id"`
	StartedAt time.Time          `json:"started_at"`
	EndedAt   time.Time          `json:"ended_at"`
	Results   []ComplianceResult `json:"results"`
	Summary   ComplianceSummary  `json:"summary"`
}

// ResultFilter holds query parameters for GET /results.
type ResultFilter struct {
	ProbeID  string
	Status   string
	Category string
	CheckID  string
	Limit    int
}
