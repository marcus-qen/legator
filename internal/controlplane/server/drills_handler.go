package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/reliability"
)

// handleListDrills returns all available drill definitions.
func (s *Server) handleListDrills(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	defs := s.drillRunner.Definitions()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"drills": defs,
		"count":  len(defs),
	})
}

// handleRunDrill triggers a named failure drill and persists the result.
func (s *Server) handleRunDrill(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetWrite) {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing drill name")
		return
	}

	ctx := r.Context()
	result := s.drillRunner.Run(ctx, reliability.DrillScenario(name))

	if s.drillStore != nil {
		if err := s.drillStore.Save(result); err != nil {
			s.logger.Sugar().Warnf("failed to persist drill result: %v", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if result.Status == reliability.DrillStatusFail {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	_ = json.NewEncoder(w).Encode(result)
}

// handleListDrillHistory returns past drill results from the SQLite store.
func (s *Server) handleListDrillHistory(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}
	if s.drillStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "drill history store unavailable")
		return
	}

	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}

	results, err := s.drillStore.List(limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if results == nil {
		results = []reliability.DrillResult{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"results": results,
		"count":   len(results),
	})
}

// initDrills sets up the drill runner and its SQLite-backed history store.
func (s *Server) initDrills() {
	s.drillRunner = reliability.NewDrillRunner(reliability.DrillRunnerDeps{})

	drillsDBPath := filepath.Join(s.cfg.DataDir, "drills.db")
	store, err := reliability.NewDrillStore(drillsDBPath)
	if err != nil {
		s.logger.Sugar().Warnf("cannot open drills database, history disabled: %v", err)
	} else {
		s.drillStore = store
		s.logger.Sugar().Infof("drill store opened: %s", drillsDBPath)
	}
}

// handleDrillsUnavailable is the fallback if the drill runner isn't initialised.
func (s *Server) handleDrillsUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusServiceUnavailable, "service_unavailable", "failure drills unavailable")
}
