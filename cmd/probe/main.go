// Legator Probe Agent — lightweight binary deployed to target infrastructure.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/marcus-qen/legator/internal/probe/agent"
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
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(ctx, os.Args[2:])
	case "run":
		err = cmdRun(ctx, os.Args[2:])
	case "service":
		err = cmdService(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "list":
		err = cmdList(os.Args[2:])
	case "info":
		err = cmdInfo(os.Args[2:])
	case "health":
		err = cmdHealth(os.Args[2:])
	case "uninstall":
		err = cmdUninstall(ctx)
	case "version":
		fmt.Printf("legator-probe %s (commit: %s, built: %s)\n", version, commit, date)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Usage: probe <command>

Commands:
  init       Register with control plane (requires --server and --token)
  run        Start the agent loop (runs as systemd service)
  service    Manage the systemd service (install|remove|status)
  status     Show local probe status
  list       List all probes in the fleet (--url, --format json)
  info       Show detailed probe info (probe info <id>)
  health     Show probe health score (probe health <id>)
  uninstall  Deregister and remove all probe files
  version    Print version information
  help       Show this help

Global flags:
  --config-dir <path>   Config directory (default /etc/legator)`)
}

// parseConfigDir extracts --config-dir from args, returning the dir and remaining args.
func parseConfigDir(args []string) (string, []string) {
	configDir := ""
	var remaining []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "--config-dir" || args[i] == "-c") && i+1 < len(args) {
			configDir = args[i+1]
			i++
		} else {
			remaining = append(remaining, args[i])
		}
	}

	if configDir == "" {
		configDir = strings.TrimSpace(os.Getenv("LEGATOR_CONFIG_DIR"))
	}

	return configDir, remaining
}

func parseProbeTags(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	tags := make([]string, 0)
	seen := make(map[string]struct{})
	for _, tag := range strings.Split(raw, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	if len(tags) == 0 {
		return nil
	}
	return tags
}

func autoInitConfigFromEnv(ctx context.Context, configDir string, logger *zap.Logger) error {
	configPath := agent.ConfigPath(configDir)
	if _, err := os.Stat(configPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check config: %w", err)
	}

	serverURL := strings.TrimSpace(os.Getenv("LEGATOR_SERVER_URL"))
	token := strings.TrimSpace(os.Getenv("LEGATOR_TOKEN"))
	if serverURL == "" || token == "" {
		return nil
	}

	tags := parseProbeTags(os.Getenv("LEGATOR_PROBE_TAGS"))
	hostnameOverride := strings.TrimSpace(os.Getenv("NODE_NAME"))

	cfg, err := agent.RegisterWithOptions(ctx, serverURL, token, logger, agent.RegisterOptions{
		HostnameOverride: hostnameOverride,
		Tags:             tags,
	})
	if err != nil {
		return fmt.Errorf("auto-register: %w", err)
	}

	if err := cfg.Save(configDir); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	logger.Info("probe auto-initialized from environment",
		zap.String("config_path", agent.ConfigPath(configDir)),
		zap.String("probe_id", cfg.ProbeID),
	)

	return nil
}

func cmdInit(ctx context.Context, args []string) error {
	configDir, args := parseConfigDir(args)

	var server, token string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--server", "-s":
			if i+1 < len(args) {
				server = args[i+1]
				i++
			}
		case "--token", "-t":
			if i+1 < len(args) {
				token = args[i+1]
				i++
			}
		}
	}
	if server == "" || token == "" {
		return fmt.Errorf("--server and --token are required\n\nUsage: probe init --server https://cp.example.com --token prb_xxx [--config-dir /path]")
	}

	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()

	hostnameOverride := strings.TrimSpace(os.Getenv("NODE_NAME"))

	fmt.Printf("Registering with %s...\n", server)
	cfg, err := agent.RegisterWithOptions(ctx, server, token, logger, agent.RegisterOptions{
		HostnameOverride: hostnameOverride,
	})
	if err != nil {
		return err
	}

	if err := cfg.Save(configDir); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("✅ Registered as probe %s\n", cfg.ProbeID)
	fmt.Printf("   Config saved to %s\n", agent.ConfigPath(configDir))
	return nil
}

func cmdRun(ctx context.Context, args []string) error {
	configDir, _ := parseConfigDir(args)

	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()

	if err := autoInitConfigFromEnv(ctx, configDir, logger); err != nil {
		return err
	}

	cfg, err := agent.LoadConfig(configDir)
	if err != nil {
		return fmt.Errorf("load config: %w (run 'probe init' first)", err)
	}

	a := agent.New(cfg, logger)
	return a.Run(ctx)
}

func cmdService(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: probe service <install|remove|status>")
	}
	switch args[0] {
	case "install":
		return agent.ServiceInstall("")
	case "remove":
		return agent.ServiceRemove()
	case "status":
		return agent.ServiceStatus()
	default:
		return fmt.Errorf("unknown service command: %s", args[0])
	}
}

func cmdStatus(args []string) error {
	configDir, _ := parseConfigDir(args)
	cfg, err := agent.LoadConfig(configDir)
	if err != nil {
		return fmt.Errorf("not configured: %w", err)
	}
	fmt.Printf("Probe ID:  %s\n", cfg.ProbeID)
	fmt.Printf("Server:    %s\n", cfg.ServerURL)
	fmt.Printf("Policy ID: %s\n", cfg.PolicyID)
	return nil
}

func cmdUninstall(ctx context.Context) error {
	// Stop and remove service first
	_ = agent.ServiceRemove()

	// Remove config and data
	for _, dir := range []string{agent.DefaultConfigDir, agent.DefaultDataDir, agent.DefaultLogDir} {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove %s: %v\n", dir, err)
		}
	}

	fmt.Println("\u2705 Probe uninstalled")
	return nil
}
