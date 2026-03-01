package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/reliability"
)

const reliabilityAuditSampleLimit = 5000

func (s *Server) buildReliabilityScorecard(window time.Duration) reliability.Scorecard {
	now := time.Now().UTC()
	if window <= 0 {
		window = reliabilityDefaultWindow
	}

	requestStats := reliability.ControlPlaneInputs{}
	if s.reliabilityTelemetry != nil {
		snap := s.reliabilityTelemetry.Snapshot(window, now)
		requestStats = reliability.ControlPlaneInputs{
			TotalRequests:       snap.TotalRequests,
			SuccessfulRequests:  snap.SuccessfulRequests,
			ServerErrorRequests: snap.ServerErrors,
			P95Latency:          snap.P95Latency,
		}
	}

	probes := s.fleetMgr.List()
	connected := 0
	for _, probe := range probes {
		status := strings.ToLower(strings.TrimSpace(probe.Status))
		if status == "online" || status == "degraded" {
			connected++
		}
	}

	commandTotal, commandSuccess := s.commandResultStats(now, window)

	return reliability.BuildScorecard(reliability.Inputs{
		Now:    now,
		Window: window,
		ControlPlane: requestStats,
		ProbeFleet: reliability.ProbeFleetInputs{
			TotalProbes:     len(probes),
			ConnectedProbes: connected,
		},
		Command: reliability.CommandInputs{
			TotalResults:      commandTotal,
			SuccessfulResults: commandSuccess,
		},
	})
}

func (s *Server) commandResultStats(now time.Time, window time.Duration) (total int, success int) {
	if window <= 0 {
		window = reliabilityDefaultWindow
	}

	events := s.queryAudit(audit.Filter{
		Type:  audit.EventCommandResult,
		Since: now.Add(-window),
		Limit: reliabilityAuditSampleLimit,
	})
	for _, evt := range events {
		exitCode, ok := extractCommandExitCode(evt.Detail)
		if !ok {
			continue
		}
		total++
		if exitCode == 0 {
			success++
		}
	}
	return total, success
}

func extractCommandExitCode(detail any) (int, bool) {
	mapDetail, ok := detail.(map[string]any)
	if !ok {
		return 0, false
	}
	value, exists := mapDetail["exit_code"]
	if !exists {
		return 0, false
	}
	return anyToInt(value)
}

func anyToInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int8:
		return int(v), true
	case int16:
		return int(v), true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case uint:
		return int(v), true
	case uint8:
		return int(v), true
	case uint16:
		return int(v), true
	case uint32:
		return int(v), true
	case uint64:
		return int(v), true
	case float32:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		i, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		i, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}

func (s *Server) handleReliabilityScorecard(w http.ResponseWriter, r *http.Request) {
	if !s.requirePermission(w, r, auth.PermFleetRead) {
		return
	}

	window := reliabilityDefaultWindow
	if rawWindow := strings.TrimSpace(r.URL.Query().Get("window")); rawWindow != "" {
		parsed, err := parseHumanDuration(rawWindow)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid window duration")
			return
		}
		window = parsed
	}
	if window > reliabilityTelemetryMaxAge {
		window = reliabilityTelemetryMaxAge
	}

	scorecard := s.buildReliabilityScorecard(window)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(scorecard)
}
