package main

import (
	"encoding/json"

	"github.com/marcus-qen/legator/internal/probe/agent"
	"fmt"
	"io"
	"os"
	"net/http"
	"strings"
	"time"
)

// probeInfo mirrors fleet.ProbeState for JSON decoding.
type probeInfo struct {
	ID          string    `json:"id"`
	Hostname    string    `json:"hostname"`
	OS          string    `json:"os"`
	Arch        string    `json:"arch"`
	Status      string    `json:"status"`
	PolicyLevel string    `json:"policy_level"`
	Registered  time.Time `json:"registered"`
	LastSeen    time.Time `json:"last_seen"`
	Tags        []string  `json:"tags,omitempty"`
	Inventory   *struct {
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		Kernel   string `json:"kernel"`
		CPUs     int    `json:"cpus"`
		MemTotal uint64 `json:"mem_total_bytes"`
		DiskTotal uint64 `json:"disk_total_bytes"`
	} `json:"inventory,omitempty"`
	Health *struct {
		Score    int      `json:"score"`
		Status   string   `json:"status"`
		Warnings []string `json:"warnings,omitempty"`
	} `json:"health,omitempty"`
}

type healthInfo struct {
	Score    int      `json:"score"`
	Status   string   `json:"status"`
	Warnings []string `json:"warnings,omitempty"`
}

func cmdList(args []string) error {
	url := ""
	jsonFmt := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--url":
			if i+1 < len(args) {
				url = args[i+1]
				i++
			}
		case "--format":
			if i+1 < len(args) && args[i+1] == "json" {
				jsonFmt = true
				i++
			}
		}
	}

	if url == "" {
		url = serverFromConfig()
	}
	if url == "" {
		return fmt.Errorf("--url required (or run 'probe init' first)")
	}

	body, err := httpGet(url + "/api/v1/probes")
	if err != nil {
		return err
	}

	var probes []probeInfo
	if err := json.Unmarshal(body, &probes); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if jsonFmt {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(probes)
	}

	// Table output
	fmt.Printf("%-14s %-16s %-10s %-12s %-22s %s\n",
		"ID", "HOSTNAME", "STATUS", "POLICY", "LAST SEEN", "TAGS")
	fmt.Println(strings.Repeat("-", 90))

	for _, p := range probes {
		id := p.ID
		if len(id) > 14 {
			id = id[:14]
		}
		host := p.Hostname
		if len(host) > 16 {
			host = host[:16]
		}
		tags := strings.Join(p.Tags, ",")
		if len(tags) > 20 {
			tags = tags[:20] + "…"
		}
		lastSeen := "-"
		if !p.LastSeen.IsZero() {
			lastSeen = p.LastSeen.Format("2006-01-02 15:04:05")
		}
		fmt.Printf("%-14s %-16s %-10s %-12s %-22s %s\n",
			id, host, p.Status, p.PolicyLevel, lastSeen, tags)
	}

	fmt.Printf("\nTotal: %d probes\n", len(probes))
	return nil
}

func cmdInfo(args []string) error {
	url := ""
	probeID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--url":
			if i+1 < len(args) {
				url = args[i+1]
				i++
			}
		case "--id":
			if i+1 < len(args) {
				probeID = args[i+1]
				i++
			}
		default:
			if probeID == "" && !strings.HasPrefix(args[i], "-") {
				probeID = args[i]
			}
		}
	}

	if url == "" {
		url = serverFromConfig()
	}
	if url == "" {
		return fmt.Errorf("--url required (or run 'probe init' first)")
	}
	if probeID == "" {
		return fmt.Errorf("probe ID required: probe info <probe-id>")
	}

	body, err := httpGet(url + "/api/v1/probes/" + probeID)
	if err != nil {
		return err
	}

	var p probeInfo
	if err := json.Unmarshal(body, &p); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	fmt.Printf("Probe: %s\n", p.ID)
	fmt.Printf("Hostname: %s\n", p.Hostname)

	healthStr := ""
	if p.Health != nil {
		healthStr = fmt.Sprintf(" (score: %d)", p.Health.Score)
	}
	fmt.Printf("Status: %s%s\n", p.Status, healthStr)
	fmt.Printf("Policy: %s\n", p.PolicyLevel)

	if p.Inventory != nil {
		fmt.Printf("OS: %s/%s\n", p.Inventory.OS, p.Inventory.Arch)
		if p.Inventory.Kernel != "" {
			fmt.Printf("Kernel: %s\n", p.Inventory.Kernel)
		}
		fmt.Printf("CPUs: %d\n", p.Inventory.CPUs)
		if p.Inventory.MemTotal > 0 {
			fmt.Printf("Memory: %.1f GiB\n", float64(p.Inventory.MemTotal)/(1024*1024*1024))
		}
		if p.Inventory.DiskTotal > 0 {
			fmt.Printf("Disk: %.1f GiB\n", float64(p.Inventory.DiskTotal)/(1024*1024*1024))
		}
	} else {
		fmt.Printf("OS: %s/%s\n", p.OS, p.Arch)
	}

	if len(p.Tags) > 0 {
		fmt.Printf("Tags: %s\n", strings.Join(p.Tags, ", "))
	}
	fmt.Printf("Last Seen: %s\n", p.LastSeen.Format(time.RFC3339))
	fmt.Printf("Registered: %s\n", p.Registered.Format(time.RFC3339))

	return nil
}

func cmdHealth(args []string) error {
	url := ""
	probeID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--url":
			if i+1 < len(args) {
				url = args[i+1]
				i++
			}
		default:
			if probeID == "" && !strings.HasPrefix(args[i], "-") {
				probeID = args[i]
			}
		}
	}

	if url == "" {
		url = serverFromConfig()
	}
	if url == "" {
		return fmt.Errorf("--url required")
	}
	if probeID == "" {
		return fmt.Errorf("probe ID required: probe health <probe-id>")
	}

	body, err := httpGet(url + "/api/v1/probes/" + probeID + "/health")
	if err != nil {
		return err
	}

	var h healthInfo
	if err := json.Unmarshal(body, &h); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	fmt.Printf("Health Score: %d/100 (%s)\n", h.Score, h.Status)
	if len(h.Warnings) > 0 {
		fmt.Println("Warnings:")
		for _, w := range h.Warnings {
			fmt.Printf("  - %s\n", w)
		}
	}

	return nil
}

func serverFromConfig() string {
	// Try to read from existing probe config
	cfg, err := agent.LoadConfig("")
	if err != nil {
		return ""
	}
	return cfg.ServerURL
}

func httpGet(url string) ([]byte, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}
