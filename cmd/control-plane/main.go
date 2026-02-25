// Legator Control Plane — the central brain that manages probe agents.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/api"
	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	cpws "github.com/marcus-qen/legator/internal/controlplane/websocket"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Template types for fleet UI
type FleetSummary struct {
	Online   int
	Offline  int
	Degraded int
	Total    int
}

type FleetPageData struct {
	Probes  []*fleet.ProbeState
	Summary FleetSummary
	Version string
	Commit  string
}

type ProbePageData struct {
	Probe  *fleet.ProbeState
	Uptime string
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"statusClass":    templateStatusClass,
		"humanizeStatus": templateHumanizeStatus,
		"formatLastSeen": formatLastSeen,
		"humanBytes":     humanBytes,
	}
}

func main() {
	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()

	cfg, err := loadConfig()
	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err))
	}

	// Load templates — look relative to binary, fall back to working dir
	tmplDir := filepath.Join("web", "templates")
	tmpl, err := template.New("").Funcs(templateFuncs()).ParseGlob(filepath.Join(tmplDir, "*.html"))
	if err != nil {
		logger.Warn("failed to load templates, UI will show fallback", zap.Error(err))
		tmpl = nil
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Core components
	fleetMgr := fleet.NewManager(logger.Named("fleet"))
	tokenStore := api.NewTokenStore()
	auditLog := audit.NewLog(10000) // 10k event ring buffer
	cmdTracker := cmdtracker.New(2 * time.Minute) // 2-min timeout for command results
	hub := cpws.NewHub(logger.Named("ws"), func(probeID string, env protocol.Envelope) {
		handleProbeMessage(fleetMgr, auditLog, cmdTracker, logger, probeID, env)
	})

	// Start offline checker
	go offlineChecker(ctx, fleetMgr)

	mux := http.NewServeMux()

	// Health + version
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"version": version, "commit": commit, "date": date,
		})
	})

	// Fleet API
	mux.HandleFunc("GET /api/v1/probes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fleetMgr.List())
	})
	mux.HandleFunc("GET /api/v1/probes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ps, ok := fleetMgr.Get(id)
		if !ok {
			http.Error(w, `{"error":"probe not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ps)
	})
	mux.HandleFunc("POST /api/v1/probes/{id}/command", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ps, ok := fleetMgr.Get(id)
		if !ok {
			http.Error(w, `{"error":"probe not found"}`, http.StatusNotFound)
			return
		}

		var cmd protocol.CommandPayload
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
			return
		}

		// Generate request ID if not provided
		if cmd.RequestID == "" {
			cmd.RequestID = fmt.Sprintf("cmd-%d", time.Now().UnixNano()%100000)
		}

		// Check if caller wants to wait for result (?wait=true)
		wantWait := r.URL.Query().Get("wait") == "true" || r.URL.Query().Get("wait") == "1"

		// Track the command before dispatching
		var pending *cmdtracker.PendingCommand
		if wantWait {
			pending = cmdTracker.Track(cmd.RequestID, id, cmd.Command, ps.PolicyLevel)
		}

		if err := hub.SendTo(id, protocol.MsgCommand, cmd); err != nil {
			if pending != nil {
				cmdTracker.Cancel(cmd.RequestID)
			}
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
			return
		}

		if !wantWait {
			// Fire-and-forget mode (backwards compatible)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status":     "dispatched",
				"request_id": cmd.RequestID,
			})
			return
		}

		// Synchronous mode: wait for probe result
		timeout := 30 * time.Second
		if cmd.Timeout > 0 {
			timeout = cmd.Timeout + 5*time.Second
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case result := <-pending.Result:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
		case <-timer.C:
			cmdTracker.Cancel(cmd.RequestID)
			http.Error(w, `{"error":"timeout waiting for probe response"}`, http.StatusGatewayTimeout)
		case <-r.Context().Done():
			cmdTracker.Cancel(cmd.RequestID)
		}
	})

	// Registration
	mux.HandleFunc("POST /api/v1/register", api.HandleRegisterWithAudit(tokenStore, fleetMgr, auditLog, logger.Named("register")))
	mux.HandleFunc("POST /api/v1/tokens", api.HandleGenerateTokenWithAudit(tokenStore, auditLog, logger.Named("tokens")))

	// Fleet summary
	mux.HandleFunc("GET /api/v1/fleet/summary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"counts":    fleetMgr.Count(),
			"connected": hub.Connected(),
		})
	})

	// Approval queue (stub for now)
	mux.HandleFunc("GET /api/v1/approvals", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"approvals":[]}`)
	})

	// Audit log
	mux.HandleFunc("GET /api/v1/audit", func(w http.ResponseWriter, r *http.Request) {
		probeID := r.URL.Query().Get("probe_id")
		limit := 50
		events := auditLog.Query(audit.Filter{ProbeID: probeID, Limit: limit})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "total": auditLog.Count()})
	})

	// Pending commands
	mux.HandleFunc("GET /api/v1/commands/pending", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pending":   cmdTracker.ListPending(),
			"in_flight": cmdTracker.InFlight(),
		})
	})

	// WebSocket endpoint for probes
	mux.HandleFunc("GET /ws/probe", hub.HandleProbeWS)

	// Static assets
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join("web", "static")))))

	// Fleet UI (root)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if tmpl == nil {
			// Fallback when templates aren't available
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Legator Control Plane</title></head>
<body>
<h1>Legator Control Plane</h1>
<p>Version: %s (%s)</p>
<p><a href="/api/v1/probes">Fleet API</a> | <a href="/api/v1/fleet/summary">Summary</a></p>
</body></html>`, version, commit)
			return
		}

		probes := fleetMgr.List()
		sort.Slice(probes, func(i, j int) bool {
			lhs := strings.ToLower(probes[i].Hostname)
			if lhs == "" {
				lhs = probes[i].ID
			}
			rhs := strings.ToLower(probes[j].Hostname)
			if rhs == "" {
				rhs = probes[j].ID
			}
			return lhs < rhs
		})

		counts := fleetMgr.Count()
		data := FleetPageData{
			Probes: probes,
			Summary: FleetSummary{
				Online:   counts["online"],
				Offline:  counts["offline"],
				Degraded: counts["degraded"],
				Total:    len(probes),
			},
			Version: version,
			Commit:  commit,
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "fleet.html", data); err != nil {
			logger.Error("failed to render fleet page", zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	})

	// Probe detail UI
	mux.HandleFunc("GET /probe/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ps, ok := fleetMgr.Get(id)
		if !ok {
			ps = &fleet.ProbeState{
				ID:          id,
				Status:      "offline",
				PolicyLevel: protocol.CapObserve,
			}
		}

		if tmpl == nil {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<h1>Probe: %s</h1><p>Status: %s</p>`, id, ps.Status)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := ProbePageData{
			Probe:  ps,
			Uptime: calculateUptime(ps.Registered),
		}
		if err := tmpl.ExecuteTemplate(w, "probe-detail.html", data); err != nil {
			logger.Error("failed to render probe detail", zap.String("probe", id), zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	})

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	logger.Info("starting control plane",
		zap.String("addr", cfg.ListenAddr),
		zap.String("version", version),
	)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("server error", zap.Error(err))
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", zap.Error(err))
	}
}

func handleProbeMessage(fm *fleet.Manager, al *audit.Log, ct *cmdtracker.Tracker, logger *zap.Logger, probeID string, env protocol.Envelope) {
	switch env.Type {
	case protocol.MsgHeartbeat:
		data, _ := json.Marshal(env.Payload)
		var hb protocol.HeartbeatPayload
		if err := json.Unmarshal(data, &hb); err != nil {
			logger.Warn("bad heartbeat payload", zap.String("probe", probeID), zap.Error(err))
			return
		}
		if err := fm.Heartbeat(probeID, &hb); err != nil {
			// Auto-register on first heartbeat from unknown probe
			fm.Register(probeID, "", "", "")
			_ = fm.Heartbeat(probeID, &hb)
			al.Emit(audit.EventProbeRegistered, probeID, "system", "Auto-registered via heartbeat")
		}

	case protocol.MsgInventory:
		data, _ := json.Marshal(env.Payload)
		var inv protocol.InventoryPayload
		if err := json.Unmarshal(data, &inv); err != nil {
			logger.Warn("bad inventory payload", zap.String("probe", probeID), zap.Error(err))
			return
		}
		if err := fm.UpdateInventory(probeID, &inv); err != nil {
			logger.Warn("inventory update failed", zap.String("probe", probeID), zap.Error(err))
		} else {
			al.Emit(audit.EventInventoryUpdate, probeID, probeID, "Inventory updated")
		}

	case protocol.MsgCommandResult:
		data, _ := json.Marshal(env.Payload)
		var result protocol.CommandResultPayload
		if err := json.Unmarshal(data, &result); err != nil {
			logger.Warn("bad command result payload", zap.String("probe", probeID), zap.Error(err))
			return
		}
		logger.Info("command result received",
			zap.String("probe", probeID),
			zap.String("request_id", result.RequestID),
			zap.Int("exit_code", result.ExitCode),
		)
		al.Record(audit.Event{
			Type:    audit.EventCommandResult,
			ProbeID: probeID,
			Actor:   probeID,
			Summary: "Command completed: " + result.RequestID,
			Detail:  map[string]any{"exit_code": result.ExitCode, "duration_ms": result.Duration},
		})
		// Route result to waiting HTTP caller (if any)
		if err := ct.Complete(result.RequestID, &result); err != nil {
			logger.Debug("no waiting caller for result", zap.String("request_id", result.RequestID))
		}

	default:
		logger.Debug("unhandled message type",
			zap.String("probe", probeID),
			zap.String("type", string(env.Type)),
		)
	}
}

func offlineChecker(ctx context.Context, fm *fleet.Manager) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fm.MarkOffline(60 * time.Second)
		}
	}
}

// Template helper functions

func formatLastSeen(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func templateStatusClass(status string) string {
	switch strings.ToLower(status) {
	case "online":
		return "online"
	case "offline":
		return "offline"
	case "degraded":
		return "degraded"
	default:
		return "pending"
	}
}

func templateHumanizeStatus(status string) string {
	s := strings.ToLower(status)
	if s == "" {
		return "pending"
	}
	return s
}

func humanBytes(v uint64) string {
	if v == 0 {
		return "0 B"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	value := float64(v)
	unit := 0
	for unit < len(units)-1 && value >= 1024 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%.0f %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

func calculateUptime(start time.Time) string {
	if start.IsZero() {
		return "n/a"
	}
	secs := int64(time.Since(start).Seconds())
	if secs < 60 {
		return strconv.FormatInt(secs, 10) + "s"
	}
	mins := secs / 60
	secs %= 60
	hours := mins / 60
	mins %= 60
	days := hours / 24
	hours %= 24

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", secs))
	}
	return strings.Join(parts, " ")
}

type Config struct {
	ListenAddr string
	DataDir    string
}

func loadConfig() (*Config, error) {
	addr := os.Getenv("LEGATOR_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	dataDir := os.Getenv("LEGATOR_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/legator"
	}
	return &Config{ListenAddr: addr, DataDir: dataDir}, nil
}
