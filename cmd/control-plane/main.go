// Legator Control Plane — the central brain that manages probe agents.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/api"
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

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	cfg, err := loadConfig()
	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Core components
	fleetMgr := fleet.NewManager(logger.Named("fleet"))
	tokenStore := api.NewTokenStore()
	hub := cpws.NewHub(logger.Named("ws"), func(probeID string, env protocol.Envelope) {
		handleProbeMessage(fleetMgr, logger, probeID, env)
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
		json.NewEncoder(w).Encode(map[string]string{
			"version": version, "commit": commit, "date": date,
		})
	})

	// Fleet API
	mux.HandleFunc("GET /api/v1/probes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fleetMgr.List())
	})
	mux.HandleFunc("GET /api/v1/probes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ps, ok := fleetMgr.Get(id)
		if !ok {
			http.Error(w, `{"error":"probe not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ps)
	})
	mux.HandleFunc("POST /api/v1/probes/{id}/command", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, ok := fleetMgr.Get(id); !ok {
			http.Error(w, `{"error":"probe not found"}`, http.StatusNotFound)
			return
		}

		var cmd protocol.CommandPayload
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
			return
		}

		if err := hub.SendTo(id, protocol.MsgCommand, cmd); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":     "dispatched",
			"request_id": cmd.RequestID,
		})
	})

	// Registration
	mux.HandleFunc("POST /api/v1/register", api.HandleRegister(tokenStore, fleetMgr, logger.Named("register")))
	mux.HandleFunc("POST /api/v1/tokens", api.HandleGenerateToken(tokenStore, logger.Named("tokens")))

	// Fleet summary
	mux.HandleFunc("GET /api/v1/fleet/summary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"counts":    fleetMgr.Count(),
			"connected": hub.Connected(),
		})
	})

	// Approval queue (stub for now)
	mux.HandleFunc("GET /api/v1/approvals", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"approvals":[]}`)
	})

	// Audit log (stub for now)
	mux.HandleFunc("GET /api/v1/audit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"events":[]}`)
	})

	// WebSocket endpoint for probes
	mux.HandleFunc("GET /ws/probe", hub.HandleProbeWS)

	// Web UI placeholder
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Legator Control Plane</title></head>
<body>
<h1>Legator Control Plane</h1>
<p>Version: %s (%s)</p>
<p><a href="/api/v1/probes">Fleet API</a> | <a href="/api/v1/fleet/summary">Summary</a></p>
</body></html>`, version, commit)
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

func handleProbeMessage(fm *fleet.Manager, logger *zap.Logger, probeID string, env protocol.Envelope) {
	switch env.Type {
	case protocol.MsgHeartbeat:
		data, _ := json.Marshal(env.Payload)
		var hb protocol.HeartbeatPayload
		json.Unmarshal(data, &hb)
		if err := fm.Heartbeat(probeID, &hb); err != nil {
			// Auto-register on first heartbeat from unknown probe
			fm.Register(probeID, "", "", "")
			fm.Heartbeat(probeID, &hb)
		}

	case protocol.MsgInventory:
		data, _ := json.Marshal(env.Payload)
		var inv protocol.InventoryPayload
		json.Unmarshal(data, &inv)
		if err := fm.UpdateInventory(probeID, &inv); err != nil {
			logger.Warn("inventory update failed", zap.String("probe", probeID), zap.Error(err))
		}

	case protocol.MsgCommandResult:
		data, _ := json.Marshal(env.Payload)
		var result protocol.CommandResultPayload
		json.Unmarshal(data, &result)
		logger.Info("command result received",
			zap.String("probe", probeID),
			zap.String("request_id", result.RequestID),
			zap.Int("exit_code", result.ExitCode),
		)
		// TODO: route result to requesting user/chat session

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
