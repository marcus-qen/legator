package compliance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ExportHandler exposes compliance export HTTP endpoints.
type ExportHandler struct {
	store     *Store
	scheduler *ExportScheduler
}

// NewExportHandler creates a new ExportHandler.
func NewExportHandler(store *Store, scheduler *ExportScheduler) *ExportHandler {
	return &ExportHandler{store: store, scheduler: scheduler}
}

// HandleExportCSV handles GET /api/v1/compliance/export/csv
// Query params: probes (comma-separated), category, since (RFC3339), until (RFC3339)
func (h *ExportHandler) HandleExportCSV(w http.ResponseWriter, r *http.Request) {
	filter, err := parseExportFilter(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}

	filename := fmt.Sprintf("compliance-%s.csv", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if err := WriteCSV(h.store, filter, w); err != nil {
		// Headers already sent; we can't change status code, but log is implicit
		_ = err
	}
}

// HandleExportPDF handles GET /api/v1/compliance/export/pdf
// Query params: probes (comma-separated), category, since, until, scope
func (h *ExportHandler) HandleExportPDF(w http.ResponseWriter, r *http.Request) {
	filter, err := parseExportFilter(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}

	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "all probes"
	}

	filename := fmt.Sprintf("compliance-%s.pdf", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)

	if err := WritePDF(h.store, filter, scope, w); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "pdf_error", err.Error())
	}
}

// HandleListExports handles GET /api/v1/compliance/exports
// Returns metadata for recent stored exports.
func (h *ExportHandler) HandleListExports(w http.ResponseWriter, r *http.Request) {
	exports, err := h.store.ListExports(100)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	items := make([]map[string]any, 0, len(exports))
	for _, exp := range exports {
		items = append(items, exportRecordToMap(exp))
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"exports": items,
		"total":   len(items),
	})
}

// HandleGetExport handles GET /api/v1/compliance/exports/{id}
// Returns the raw export file content (CSV or PDF).
func (h *ExportHandler) HandleGetExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_id", "export id is required")
		return
	}

	rec, data, err := h.store.GetExport(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}

	if rec.Status == "error" {
		writeJSONError(w, http.StatusUnprocessableEntity, "export_failed", rec.ErrorMsg)
		return
	}

	var contentType string
	var ext string
	switch rec.Format {
	case ExportFormatCSV:
		contentType = "text/csv; charset=utf-8"
		ext = "csv"
	case ExportFormatPDF:
		contentType = "application/pdf"
		ext = "pdf"
	default:
		contentType = "application/octet-stream"
		ext = "bin"
	}

	filename := fmt.Sprintf("compliance-%s.%s", rec.CreatedAt.UTC().Format("20060102-150405"), ext)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	_, _ = w.Write(data)
}

// parseExportFilter parses export filter query parameters from an HTTP request.
func parseExportFilter(r *http.Request) (ExportFilter, error) {
	q := r.URL.Query()
	filter := ExportFilter{}

	if probes := q.Get("probes"); probes != "" {
		for _, p := range strings.Split(probes, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				filter.ProbeIDs = append(filter.ProbeIDs, p)
			}
		}
	}

	filter.Category = strings.TrimSpace(q.Get("category"))

	if since := q.Get("since"); since != "" {
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return ExportFilter{}, fmt.Errorf("invalid since parameter: %w", err)
		}
		filter.Since = t
	}

	if until := q.Get("until"); until != "" {
		t, err := time.Parse(time.RFC3339, until)
		if err != nil {
			return ExportFilter{}, fmt.Errorf("invalid until parameter: %w", err)
		}
		filter.Until = t
	}

	return filter, nil
}
