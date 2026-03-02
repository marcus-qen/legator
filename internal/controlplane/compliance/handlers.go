package compliance

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Handler exposes compliance HTTP endpoints.
type Handler struct {
	scanner *Scanner
	store   *Store
}

// NewHandler creates a new compliance Handler.
func NewHandler(scanner *Scanner, store *Store) *Handler {
	return &Handler{scanner: scanner, store: store}
}

// HandleScan handles POST /api/v1/compliance/scan.
// Triggers a compliance scan across the fleet (or specified probes/tags).
func (h *Handler) HandleScan(w http.ResponseWriter, r *http.Request) {
	var req ScanRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
			return
		}
	}

	resp := h.scanner.Scan(r.Context(), req)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleResults handles GET /api/v1/compliance/results.
// Returns latest compliance results, filterable by probe_id, status, category, check_id.
func (h *Handler) HandleResults(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := ResultFilter{
		ProbeID:  q.Get("probe_id"),
		Status:   q.Get("status"),
		Category: q.Get("category"),
		CheckID:  q.Get("check_id"),
	}
	if l := q.Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			filter.Limit = v
		}
	}

	results, err := h.store.ListResults(filter)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if results == nil {
		results = []ComplianceResult{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results": results,
		"total":   len(results),
	})
}

// HandleSummary handles GET /api/v1/compliance/summary.
// Returns fleet-wide compliance score and breakdown by category.
func (h *Handler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := h.store.Summary()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summary)
}

// HandleChecks handles GET /api/v1/compliance/checks.
// Returns the list of available compliance checks (without CheckFunc).
func (h *Handler) HandleChecks(w http.ResponseWriter, r *http.Request) {
	checks := h.scanner.Checks()

	// Return as JSON — CheckFunc is excluded via json:"-" tag.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"checks": checks,
		"total":  len(checks),
	})
}

func writeJSONError(w http.ResponseWriter, code int, errKey, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   errKey,
		"message": message,
	})
}
