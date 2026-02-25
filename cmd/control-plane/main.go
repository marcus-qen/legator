// Legator Control Plane — the central brain that manages probe agents.
//
// Runs as a standalone binary. Serves:
//   - Web UI (fleet view, per-probe console, approval queue)
//   - REST API (fleet management, policy, audit)
//   - WebSocket server (probe connections)
//   - LLM integration (conversational management)
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	// Version
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"version":"%s","commit":"%s","date":"%s"}`+"\n", version, commit, date)
	})

	// API routes (TODO: wire real handlers)
	mux.HandleFunc("GET /api/v1/probes", stubHandler("list probes"))
	mux.HandleFunc("GET /api/v1/probes/{id}", stubHandler("get probe"))
	mux.HandleFunc("POST /api/v1/probes/{id}/command", stubHandler("send command"))
	mux.HandleFunc("GET /api/v1/audit", stubHandler("audit log"))
	mux.HandleFunc("GET /api/v1/policies", stubHandler("list policies"))
	mux.HandleFunc("GET /api/v1/approvals", stubHandler("approval queue"))
	mux.HandleFunc("POST /api/v1/approvals/{id}/decide", stubHandler("approve/deny"))

	// WebSocket endpoint for probe connections
	mux.HandleFunc("/ws/probe", stubHandler("probe websocket"))

	// Web UI (TODO: serve embedded or filesystem templates)
	mux.HandleFunc("GET /", stubHandler("fleet dashboard"))

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

func stubHandler(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"stub":"%s","status":"not_implemented"}`+"\n", name)
	}
}

// Config holds control plane configuration.
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
	return &Config{
		ListenAddr: addr,
		DataDir:    dataDir,
	}, nil
}
