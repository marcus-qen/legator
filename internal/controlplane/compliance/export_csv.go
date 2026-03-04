package compliance

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"time"
)

// ExportFilter holds parameters for filtering compliance results for export.
type ExportFilter struct {
	ProbeIDs []string  // filter to specific probe IDs (empty = all)
	Category string    // filter to specific category (empty = all)
	Since    time.Time // include results at or after this time (zero = no lower bound)
	Until    time.Time // include results at or before this time (zero = no upper bound)
	Limit    int       // max rows (0 = default 10000)
}

// ExportFormat identifies the export file format.
type ExportFormat string

const (
	ExportFormatCSV ExportFormat = "csv"
	ExportFormatPDF ExportFormat = "pdf"
)

// ExportRecord stores metadata about a completed scheduled export.
type ExportRecord struct {
	ID        string       `json:"id"`
	Format    ExportFormat `json:"format"`
	CreatedAt time.Time    `json:"created_at"`
	SizeBytes int64        `json:"size_bytes"`
	Status    string       `json:"status"` // "ok" or "error"
	ErrorMsg  string       `json:"error,omitempty"`
	// Filter params captured at generation time.
	ProbeIDs []string `json:"probe_ids,omitempty"`
	Category string   `json:"category,omitempty"`
	Since    string   `json:"since,omitempty"` // RFC3339
	Until    string   `json:"until,omitempty"` // RFC3339
}

// csvColumns defines the CSV header row.
var csvColumns = []string{
	"probe_id", "check_id", "check_name", "category",
	"severity", "status", "evidence", "timestamp",
}

// WriteCSV writes compliance results matching filter to w as UTF-8 CSV.
// The first row is always the header. Uses the historical table when Since or Until
// are set; otherwise queries latest results.
func WriteCSV(store *Store, filter ExportFilter, w io.Writer) error {
	results, err := fetchForExport(store, filter)
	if err != nil {
		return fmt.Errorf("fetch results: %w", err)
	}

	cw := csv.NewWriter(w)
	if err := cw.Write(csvColumns); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	for _, r := range results {
		row := []string{
			r.ProbeID,
			r.CheckID,
			r.CheckName,
			r.Category,
			r.Severity,
			r.Status,
			r.Evidence,
			r.Timestamp.UTC().Format(time.RFC3339),
		}
		if err := cw.Write(row); err != nil {
			return fmt.Errorf("write row: %w", err)
		}
	}
	cw.Flush()
	return cw.Error()
}

// fetchForExport retrieves results according to the filter.
// When a time range is provided it queries the history table; otherwise latest results.
func fetchForExport(store *Store, filter ExportFilter) ([]ComplianceResult, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 10000
	}

	if !filter.Since.IsZero() || !filter.Until.IsZero() {
		return store.ListHistory(filter)
	}

	// Build a ResultFilter from ExportFilter.
	rf := ResultFilter{
		Category: filter.Category,
		Limit:    limit,
	}
	// When multiple probe IDs are requested we do a loop; single probe uses direct filter.
	if len(filter.ProbeIDs) == 1 {
		rf.ProbeID = filter.ProbeIDs[0]
		return store.ListResults(rf)
	}
	if len(filter.ProbeIDs) == 0 {
		return store.ListResults(rf)
	}
	// Multiple probes: union per probe (naïve but correct).
	var all []ComplianceResult
	for _, pid := range filter.ProbeIDs {
		rf.ProbeID = pid
		rows, err := store.ListResults(rf)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
	}
	return all, nil
}

// buildExportFilterDescription returns a human-readable summary of the filter.
func buildExportFilterDescription(filter ExportFilter) string {
	parts := []string{}
	if len(filter.ProbeIDs) > 0 {
		parts = append(parts, "probes: "+strings.Join(filter.ProbeIDs, ", "))
	}
	if filter.Category != "" {
		parts = append(parts, "category: "+filter.Category)
	}
	if !filter.Since.IsZero() {
		parts = append(parts, "since: "+filter.Since.Format(time.RFC3339))
	}
	if !filter.Until.IsZero() {
		parts = append(parts, "until: "+filter.Until.Format(time.RFC3339))
	}
	if len(parts) == 0 {
		return "all results"
	}
	return strings.Join(parts, " | ")
}
