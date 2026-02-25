// Legator Probe Agent — lightweight binary deployed to target infrastructure.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/marcus-qen/legator/internal/probe/agent"
	"go.uber.org/zap"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

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
		err = cmdStatus()
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
  uninstall  Deregister and remove all probe files
  version    Print version information
  help       Show this help`)
}

func cmdInit(ctx context.Context, args []string) error {
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
		return fmt.Errorf("--server and --token are required\n\nUsage: probe init --server https://cp.example.com --token prb_xxx")
	}

	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()

	fmt.Printf("Registering with %s...\n", server)
	cfg, err := agent.Register(ctx, server, token, logger)
	if err != nil {
		return err
	}

	if err := cfg.Save(""); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("✅ Registered as probe %s\n", cfg.ProbeID)
	fmt.Printf("   Config saved to %s\n", agent.ConfigPath(""))
	return nil
}

func cmdRun(ctx context.Context, args []string) error {
	// Parse optional --config-dir
	configDir := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--config-dir" && i+1 < len(args) {
			configDir = args[i+1]
			i++
		}
	}

	cfg, err := agent.LoadConfig(configDir)
	if err != nil {
		return fmt.Errorf("load config: %w (run 'probe init' first)", err)
	}

	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()

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
		return agent.ServiceInstall("")
	case "status":
		return agent.ServiceInstall("")
	default:
		return fmt.Errorf("unknown service command: %s", args[0])
	}
}

func cmdStatus() error {
	cfg, err := agent.LoadConfig("")
	if err != nil {
		return fmt.Errorf("not configured: %w", err)
	}
	fmt.Printf("Probe ID:  %s\n", cfg.ProbeID)
	fmt.Printf("Server:    %s\n", cfg.ServerURL)
	fmt.Printf("Policy ID: %s\n", cfg.PolicyID)
	return nil
}

func cmdUninstall(ctx context.Context) error {
	return agent.ServiceInstall("")
}
