// Legator Control Plane — the central brain that manages probe agents.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/config"
	"github.com/marcus-qen/legator/internal/controlplane/server"
	"go.uber.org/zap"
)

var (
	version string
	commit  string
	date    string
)

func init() {
	if version == "" {
		version = "dev"
	}
	if commit == "" {
		commit = "unknown"
	}
	if date == "" {
		date = buildTimestamp()
	}
}

func buildTimestamp() string {
	exePath, err := os.Executable()
	if err == nil {
		if info, statErr := os.Stat(exePath); statErr == nil {
			return info.ModTime().UTC().Format(time.RFC3339)
		}
	}

	return time.Now().UTC().Format(time.RFC3339)
}

func main() {
	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()

	// Inject build info
	server.Version = version
	server.Commit = commit
	server.Date = date

	cfg, err := loadConfig()
	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err))
	}

	srv, err := server.New(*cfg, logger)
	if err != nil {
		logger.Fatal("failed to create server", zap.Error(err))
	}
	defer srv.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := srv.Run(ctx); err != nil {
		logger.Fatal("server error", zap.Error(err))
	}
}

func loadConfig() (*config.Config, error) {
	configPath := ""
	for i, arg := range os.Args {
		if arg == "--config" && i+1 < len(os.Args) {
			configPath = os.Args[i+1]
		}
	}
	for _, arg := range os.Args {
		if arg == "init-config" {
			cfg := config.Default()
			path := "legator.json"
			if configPath != "" {
				path = configPath
			}
			if err := cfg.Save(path); err != nil {
				return nil, fmt.Errorf("save config: %w", err)
			}
			fmt.Printf("Config written to %s\n", path)
			os.Exit(0)
		}
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}
