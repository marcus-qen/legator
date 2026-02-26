package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const (
	defaultServer = "http://localhost:8080"
)

type cliConfig struct {
	server     string
	apiKey     string
	jsonOutput bool
}

func main() {
	cfg, command, args, err := parseArgs(os.Args[1:])
	if errors.Is(err, errShowUsage) {
		printUsage()
		if len(os.Args) == 1 {
			os.Exit(1)
		}
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		printUsage()
		os.Exit(1)
	}

	if command == "" {
		printUsage()
		os.Exit(1)
	}

	client := NewAPIClient(cfg.server, cfg.apiKey)
	ctx := context.Background()

	switch command {
	case "fleet":
		err = runFleet(ctx, client, cfg, args)
	case "probes":
		err = runProbes(ctx, client, cfg, args)
	case "probe":
		err = runProbe(ctx, client, cfg, args)
	case "command":
		err = runCommand(ctx, client, cfg, args)
	case "tokens":
		err = runTokens(ctx, client, cfg, args)
	case "keys":
		err = runKeys(ctx, client, cfg, args)
	case "version":
		fmt.Printf("legatorctl %s (commit: %s, built: %s)\n", version, commit, date)
		return
	case "help", "--help", "-h":
		printUsage()
	default:
		err = fmt.Errorf("unknown command: %s", command)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

var errShowUsage = errors.New("show usage")

func parseArgs(args []string) (cliConfig, string, []string, error) {
	cfg := cliConfig{
		server:     defaultServer,
		apiKey:     os.Getenv("LEGATOR_API_KEY"),
		jsonOutput: false,
	}

	idx := 0
	for idx < len(args) {
		arg := args[idx]
		if !strings.HasPrefix(arg, "-") {
			break
		}
		switch arg {
		case "--help", "-h":
			return cfg, "", nil, errShowUsage
		case "--server", "-s":
			if idx+1 >= len(args) {
				return cfg, "", nil, fmt.Errorf("--server requires a value")
			}
			cfg.server = args[idx+1]
			idx += 2
		case "--api-key":
			if idx+1 >= len(args) {
				return cfg, "", nil, fmt.Errorf("--api-key requires a value")
			}
			cfg.apiKey = args[idx+1]
			idx += 2
		case "--json":
			cfg.jsonOutput = true
			idx++
		default:
			return cfg, "", nil, fmt.Errorf("unknown flag: %s", arg)
		}
	}

	if idx >= len(args) {
		return cfg, "", nil, errShowUsage
	}

	return cfg, args[idx], args[idx+1:], nil
}

func printUsage() {
	fmt.Print(`Usage: legatorctl [--server <url>] [--api-key <key>] [--json] <command>

Commands:
  fleet                     Show fleet summary
  probes                    List all probes
  probe <id>                Show probe details
  command <id> <cmd> ...    Send command to a probe
  tokens create             Generate a registration token
  keys list                 List API keys
  keys create --name <name> --perms <perms>
                            Create a new API key
`)
}

func runFleet(ctx context.Context, client *APIClient, cfg cliConfig, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: legatorctl fleet")
	}

	summary, err := client.FleetSummary(ctx)
	if err != nil {
		return err
	}
	if cfg.jsonOutput {
		return PrintJSON(os.Stdout, summary)
	}

	online := 0
	offline := 0
	degraded := 0
	if summary.Counts != nil {
		online = summary.Counts["online"]
		offline = summary.Counts["offline"]
		degraded = summary.Counts["degraded"]
	}
	total := online + offline + degraded

	headers := []string{"STATUS", "COUNT"}
	rows := [][]string{
		{"online", strconv.Itoa(online)},
		{"offline", strconv.Itoa(offline)},
		{"degraded", strconv.Itoa(degraded)},
		{"connected", strconv.Itoa(summary.Connected)},
		{"pending_approvals", strconv.Itoa(summary.PendingApprovals)},
		{"total", strconv.Itoa(total)},
	}

	for i, row := range rows {
		if row[0] == "online" || row[0] == "offline" || row[0] == "degraded" {
			rows[i][0] = ColorStatus(row[0])
		}
	}
	RenderTable(os.Stdout, headers, rows)
	return nil
}

func runProbes(ctx context.Context, client *APIClient, cfg cliConfig, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: legatorctl probes")
	}

	probes, err := client.Probes(ctx)
	if err != nil {
		return err
	}

	if cfg.jsonOutput {
		return PrintJSON(os.Stdout, probes)
	}

	headers := []string{"ID", "HOSTNAME", "STATUS", "POLICY", "LAST SEEN", "OS/ARCH", "TAGS"}
	rows := make([][]string, 0, len(probes))

	for _, p := range probes {
		status := ColorStatus(p.Status)
		host := Truncate(p.Hostname, 18)
		policy := p.PolicyLevel
		if policy == "" {
			policy = "-"
		}
		lastSeen := FormatTimeOrDash(p.LastSeen)
		osArch := fmt.Sprintf("%s/%s", p.OS, p.Arch)
		if osArch == "/" {
			osArch = "-"
		}
		rows = append(rows, []string{
			Truncate(p.ID, 18),
			host,
			status,
			policy,
			lastSeen,
			osArch,
			Truncate(strings.Join(p.Tags, ","), 24),
		})
	}

	RenderTable(os.Stdout, headers, rows)
	fmt.Fprintf(os.Stdout, "\nTotal: %d probes\n", len(probes))
	return nil
}

func runProbe(ctx context.Context, client *APIClient, cfg cliConfig, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: legatorctl probe <id>")
	}
	probeID := args[0]

	probe, err := client.Probe(ctx, probeID)
	if err != nil {
		return err
	}

	if cfg.jsonOutput {
		return PrintJSON(os.Stdout, probe)
	}

	osValue := probe.OS
	archValue := probe.Arch
	if probe.Inventory != nil {
		if probe.Inventory.OS != "" {
			osValue = probe.Inventory.OS
		}
		if probe.Inventory.Arch != "" {
			archValue = probe.Inventory.Arch
		}
	}

	fmt.Printf("ID: %s\n", probe.ID)
	fmt.Printf("Hostname: %s\n", probe.Hostname)
	fmt.Printf("Status: %s\n", ColorStatus(probe.Status))
	fmt.Printf("Policy: %s\n", probe.PolicyLevel)
	fmt.Printf("Last Seen: %s\n", FormatTimeOrDash(probe.LastSeen))
	fmt.Printf("Registered: %s\n", FormatTimeOrDash(probe.Registered))
	fmt.Printf("OS/Arch: %s/%s\n", osValue, archValue)

	if probe.Inventory != nil {
		if probe.Inventory.Kernel != "" {
			fmt.Printf("Kernel: %s\n", probe.Inventory.Kernel)
		}
		if probe.Inventory.CPUs > 0 {
			fmt.Printf("CPUs: %d\n", probe.Inventory.CPUs)
		}
		if probe.Inventory.MemTotal > 0 {
			fmt.Printf("Memory: %.2f GiB\n", float64(probe.Inventory.MemTotal)/(1024*1024*1024))
		}
		if probe.Inventory.DiskTotal > 0 {
			fmt.Printf("Disk: %.2f GiB\n", float64(probe.Inventory.DiskTotal)/(1024*1024*1024))
		}
	}

	if len(probe.Tags) > 0 {
		fmt.Printf("Tags: %s\n", strings.Join(probe.Tags, ", "))
	}
	if probe.Health != nil {
		fmt.Printf("Health: %s (%d/100)\n", probe.Health.Status, probe.Health.Score)
		if len(probe.Health.Warnings) > 0 {
			fmt.Println("Warnings:")
			for _, warning := range probe.Health.Warnings {
				fmt.Fprintf(os.Stdout, "- %s\n", warning)
			}
		}
	}

	return nil
}

func runCommand(ctx context.Context, client *APIClient, cfg cliConfig, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: legatorctl command <id> <cmd> [args...]")
	}
	probeID := args[0]
	command := args[1]
	cmdArgs := args[2:]

	result, err := client.SendCommand(ctx, probeID, command, cmdArgs)
	if err != nil {
		return err
	}

	if cfg.jsonOutput {
		return PrintJSON(os.Stdout, result)
	}

	if result == nil {
		fmt.Println("Command sent")
		return nil
	}

	if status, ok := result["status"].(string); ok {
		fmt.Printf("Status: %s\n", status)
	}
	if reqID, ok := result["request_id"].(string); ok {
		fmt.Printf("Request ID: %s\n", reqID)
	}
	if approvalID, ok := result["approval_id"].(string); ok {
		fmt.Printf("Approval Required: %s\n", approvalID)
	}
	if msg, ok := result["message"].(string); ok {
		fmt.Printf("Message: %s\n", msg)
	}

	return nil
}

func runTokens(ctx context.Context, client *APIClient, cfg cliConfig, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: legatorctl tokens create")
	}
	if args[0] != "create" {
		return fmt.Errorf("unknown tokens command: %s", args[0])
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: legatorctl tokens create")
	}
	tok, err := client.CreateToken(ctx)
	if err != nil {
		return err
	}
	if cfg.jsonOutput {
		return PrintJSON(os.Stdout, tok)
	}

	fmt.Printf("Token: %s\n", tok.Value)
	fmt.Printf("Created: %s\n", tok.Created.Format("2006-01-02 15:04:05"))
	fmt.Printf("Expires: %s\n", tok.Expires.Format("2006-01-02 15:04:05"))
	fmt.Printf("Used: %t\n", tok.Used)
	return nil
}

func runKeys(ctx context.Context, client *APIClient, cfg cliConfig, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: legatorctl keys list|create")
	}

	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("usage: legatorctl keys list")
		}
		resp, err := client.ListKeys(ctx)
		if err != nil {
			return err
		}
		if cfg.jsonOutput {
			return PrintJSON(os.Stdout, resp)
		}

		headers := []string{"ID", "NAME", "PREFIX", "PERMISSIONS", "ENABLED", "EXPIRES"}
		rows := make([][]string, 0, len(resp.Keys))
		for _, k := range resp.Keys {
			expires := "-"
			if k.ExpiresAt != nil {
				expires = k.ExpiresAt.Format("2006-01-02 15:04:05")
			}
			rows = append(rows, []string{
				k.ID,
				k.Name,
				k.KeyPrefix,
				strings.Join(k.Permissions, ","),
				strconv.FormatBool(k.Enabled),
				expires,
			})
		}
		RenderTable(os.Stdout, headers, rows)
		fmt.Fprintf(os.Stdout, "\nTotal: %d keys\n", resp.Total)
		return nil
	case "create":
		name := ""
		permsArg := ""
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--name":
				if i+1 >= len(args) {
					return fmt.Errorf("--name requires a value")
				}
				name = args[i+1]
				i++
			case "--perms":
				if i+1 >= len(args) {
					return fmt.Errorf("--perms requires a value")
				}
				permsArg = args[i+1]
				i++
			default:
				return fmt.Errorf("unknown flag: %s", args[i])
			}
		}
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		if permsArg == "" {
			return fmt.Errorf("--perms is required")
		}

		perms := parsePerms(permsArg)
		if len(perms) == 0 {
			return fmt.Errorf("--perms must contain at least one permission")
		}

		resp, err := client.CreateKey(ctx, KeyCreatePayload{Name: name, Permissions: perms})
		if err != nil {
			return err
		}
		if cfg.jsonOutput {
			return PrintJSON(os.Stdout, resp)
		}

		fmt.Printf("Plain Key: %s\n", resp.PlainKey)
		fmt.Printf("ID: %s\n", resp.Key.ID)
		fmt.Printf("Name: %s\n", resp.Key.Name)
		fmt.Printf("Prefix: %s\n", resp.Key.KeyPrefix)
		fmt.Printf("Permissions: %s\n", strings.Join(resp.Key.Permissions, ","))
		fmt.Printf("Enabled: %t\n", resp.Key.Enabled)
		if resp.Warning != "" {
			fmt.Printf("Warning: %s\n", resp.Warning)
		}
		return nil
	default:
		return fmt.Errorf("unknown keys command: %s", args[0])
	}
}

func parsePerms(raw string) []string {
	parts := strings.Split(raw, ",")
	seen := map[string]struct{}{}
	perms := make([]string, 0, len(parts))

	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		perms = append(perms, p)
	}

	return perms
}
