// Legator Probe Agent — lightweight binary deployed to target infrastructure.
//
// Subcommands:
//   probe init       Register with control plane using a token
//   probe run        Start the agent loop (what the systemd service runs)
//   probe service    Install/remove/status of the system service
//   probe status     Local status check
//   probe uninstall  Clean removal (deregister + delete files)
//   probe version    Version info
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
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
		err = cmdRun(ctx)
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

// cmdInit registers this probe with the control plane.
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

	fmt.Printf("Registering with %s...\n", server)
	// TODO: WebSocket connect, send RegisterPayload, receive RegisteredPayload,
	//       write config to /etc/probe/config.yaml
	return fmt.Errorf("not yet implemented")
}

// cmdRun starts the main agent loop — WebSocket connection, heartbeat, command execution.
func cmdRun(ctx context.Context) error {
	// TODO: Load config, connect WebSocket, run heartbeat+inventory loop,
	//       listen for commands, enforce local policy, execute, return results.
	fmt.Println("Starting probe agent...")
	<-ctx.Done()
	fmt.Println("Shutting down.")
	return nil
}

// cmdService manages the systemd service.
func cmdService(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: probe service <install|remove|status>")
	}
	switch args[0] {
	case "install":
		// TODO: Write systemd unit, enable, start
		return fmt.Errorf("not yet implemented")
	case "remove":
		// TODO: Stop, disable, remove unit
		return fmt.Errorf("not yet implemented")
	case "status":
		// TODO: Check systemd unit status
		return fmt.Errorf("not yet implemented")
	default:
		return fmt.Errorf("unknown service command: %s", args[0])
	}
}

// cmdStatus shows local probe status.
func cmdStatus() error {
	// TODO: Read config, show probe ID, server, connection state, last heartbeat
	return fmt.Errorf("not yet implemented")
}

// cmdUninstall deregisters and removes all probe files.
func cmdUninstall(ctx context.Context) error {
	// TODO: Deregister with control plane, stop service, remove files
	return fmt.Errorf("not yet implemented")
}
