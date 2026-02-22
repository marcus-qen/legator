package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// handleRunAgent handles "legator run <agent> [--target X] [--task "..."] [--wait]"
func handleRunAgent(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: legator run <agent> [--target <device>] [--task \"...\"] [--wait]")
		os.Exit(1)
	}

	agentName := args[0]
	target := ""
	task := ""
	wait := false

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--target", "-t":
			if i+1 < len(args) {
				target = args[i+1]
				i++
			}
		case "--task":
			if i+1 < len(args) {
				task = args[i+1]
				i++
			}
		case "--wait", "-w":
			wait = true
		}
	}

	if apiClient, ok, err := tryAPIClient(); err != nil {
		fatal(err)
	} else if ok {
		handleRunAgentViaAPI(apiClient, agentName, target, task, wait)
		return
	}

	dc, defaultNS, err := getClient()
	fatal(err)

	ns := getNamespace(args)
	if ns == "" {
		ns = defaultNS
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Get the agent
	agent, err := dc.Resource(agentGVR).Namespace(ns).Get(ctx, agentName, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Agent %q not found: %v\n", agentName, err)
		os.Exit(1)
	}

	// Set annotations to trigger a run
	annotations := agent.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations["legator.io/run-now"] = "true"
	if task != "" {
		annotations["legator.io/task"] = task
	}
	if target != "" {
		annotations["legator.io/target"] = target
	}
	agent.SetAnnotations(annotations)

	_, err = dc.Resource(agentGVR).Namespace(ns).Update(ctx, agent, metav1.UpdateOptions{})
	fatal(err)

	emoji := getNestedString(*agent, "spec", "emoji")
	autonomy := getNestedString(*agent, "spec", "guardrails", "autonomy")

	fmt.Printf("%s Triggered %s (autonomy: %s)\n", emoji, agentName, autonomy)
	if task != "" {
		fmt.Printf("   Task: %s\n", task)
	}
	if target != "" {
		fmt.Printf("   Target: %s\n", target)
	}

	if !wait {
		fmt.Println("\nRun started. Use 'legator runs list --agent", agentName+"' to check progress.")
		return
	}

	// Wait for run to complete
	fmt.Println("\nWaiting for run to complete...")

	// Poll for new run (created after our trigger)
	triggerTime := time.Now()
	var runName string

	for i := 0; i < 60; i++ {
		time.Sleep(5 * time.Second)

		list, err := dc.Resource(runGVR).Namespace(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "legator.io/agent=" + agentName,
		})
		if err != nil {
			continue
		}

		for _, item := range list.Items {
			ct := item.GetCreationTimestamp()
			if ct.After(triggerTime) {
				runName = item.GetName()
				phase := getNestedString(item, "status", "phase")

				switch phase {
				case "Succeeded":
					report := getNestedString(item, "status", "report")
					fmt.Printf("\n✅ %s completed successfully\n", runName)
					if report != "" {
						fmt.Printf("\n--- Report ---\n%s\n", report)
					}
					return
				case "Failed":
					fmt.Printf("\n❌ %s failed\n", runName)
					report := getNestedString(item, "status", "report")
					if report != "" {
						fmt.Printf("\n--- Report ---\n%s\n", report)
					}
					os.Exit(1)
				default:
					fmt.Printf("  [%s] %s...\r", formatDuration(time.Since(triggerTime)), phase)
				}
			}
		}
	}

	if runName != "" {
		fmt.Printf("\n⏰ Timed out waiting. Last seen: %s\n", runName)
	} else {
		fmt.Println("\n⏰ Timed out. Run may not have started yet.")
	}
	_ = strings.TrimSpace // suppress unused import
	os.Exit(1)
}

func handleRunAgentViaAPI(apiClient *legatorAPIClient, agentName, target, task string, wait bool) {
	payload := map[string]string{}
	if task != "" {
		payload["task"] = task
	}
	if target != "" {
		payload["target"] = target
	}

	var resp map[string]any
	path := "/api/v1/agents/" + url.PathEscape(agentName) + "/run"
	if err := apiClient.postJSON(path, payload, &resp); err != nil {
		fatal(err)
	}

	fmt.Printf("🚀 Triggered %s via API\n", agentName)
	if task != "" {
		fmt.Printf("   Task: %s\n", task)
	}
	if target != "" {
		fmt.Printf("   Target: %s\n", target)
	}

	if !wait {
		fmt.Println("\nRun started. Use 'legator runs list --agent", agentName+"' to check progress.")
		return
	}

	fmt.Println("\nWaiting for run to complete...")
	triggerTime := time.Now().UTC()
	var seenRun string

	for range 60 {
		time.Sleep(5 * time.Second)

		var runsResp struct {
			Runs []struct {
				Name      string `json:"name"`
				Agent     string `json:"agent"`
				Phase     string `json:"phase"`
				CreatedAt string `json:"createdAt"`
			} `json:"runs"`
		}
		if err := apiClient.getJSON("/api/v1/runs?agent="+url.QueryEscape(agentName), &runsResp); err != nil {
			continue
		}

		for _, run := range runsResp.Runs {
			createdAt, err := time.Parse(time.RFC3339, run.CreatedAt)
			if err != nil {
				continue
			}
			if createdAt.Before(triggerTime.Add(-2 * time.Second)) {
				continue
			}

			seenRun = run.Name
			switch run.Phase {
			case "Succeeded":
				fmt.Printf("\n✅ %s completed successfully\n", run.Name)
				var detail struct {
					Status struct {
						Report string `json:"report"`
					} `json:"status"`
				}
				err := apiClient.getJSON("/api/v1/runs/"+url.PathEscape(run.Name), &detail)
				if err == nil && detail.Status.Report != "" {
					fmt.Printf("\n--- Report ---\n%s\n", detail.Status.Report)
				}
				return
			case "Failed":
				fmt.Printf("\n❌ %s failed\n", run.Name)
				var detail struct {
					Status struct {
						Report string `json:"report"`
					} `json:"status"`
				}
				err := apiClient.getJSON("/api/v1/runs/"+url.PathEscape(run.Name), &detail)
				if err == nil && detail.Status.Report != "" {
					fmt.Printf("\n--- Report ---\n%s\n", detail.Status.Report)
				}
				os.Exit(1)
			default:
				fmt.Printf("  [%s] %s...\r", formatDuration(time.Since(triggerTime)), run.Phase)
			}
		}
	}

	if seenRun != "" {
		fmt.Printf("\n⏰ Timed out waiting. Last seen: %s\n", seenRun)
	} else {
		fmt.Println("\n⏰ Timed out. Run may not have started yet.")
	}
	os.Exit(1)
}

// handleCheck handles "legator check <target>" — quick health probe
func handleCheck(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: legator check <target>")
		os.Exit(1)
	}

	target := args[0]

	// Trigger watchman-light with target
	handleRunAgent([]string{"watchman-light", "--target", target, "--task",
		fmt.Sprintf("Quick health check on %s", target), "--wait"})
}

// handleInventory handles "legator inventory [list|show <name>]"
func handleInventory(args []string) {
	if apiClient, ok, err := tryAPIClient(); err != nil {
		fatal(err)
	} else if ok {
		handleInventoryViaAPI(apiClient, args)
		return
	}

	dc, defaultNS, err := getClient()
	fatal(err)

	ns := getNamespace(args)
	if ns == "" {
		ns = defaultNS
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// List environments and extract endpoints
	envs, err := dc.Resource(envGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	fatal(err)

	if len(args) > 0 && (args[0] == "show" || args[0] == "get") {
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: legator inventory show <name>")
			os.Exit(1)
		}
		inventoryShow(envs, args[1])
		return
	}

	// List all endpoints across environments
	fmt.Println("📋 Infrastructure Inventory")
	fmt.Println("───────────────────────────")

	total := 0
	for _, env := range envs.Items {
		envName := env.GetName()
		endpoints, found, _ := unstructured.NestedMap(env.Object, "spec", "endpoints")
		if !found {
			continue
		}

		for name, ep := range endpoints {
			epMap, ok := ep.(map[string]any)
			if !ok {
				continue
			}
			url, _ := epMap["url"].(string)
			fmt.Printf("  %-25s %s  (env: %s)\n", name, url, envName)
			total++
		}
	}

	fmt.Printf("\n%d endpoints across %d environments\n", total, len(envs.Items))
}

func handleInventoryViaAPI(apiClient *legatorAPIClient, args []string) {
	var resp struct {
		Devices []map[string]any `json:"devices"`
		Total   int              `json:"total"`
		Source  string           `json:"source"`
		Sync    map[string]any   `json:"sync"`
	}
	if err := apiClient.getJSON("/api/v1/inventory", &resp); err != nil {
		fatal(err)
	}

	if len(args) > 0 && (args[0] == "show" || args[0] == "get") {
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: legator inventory show <name>")
			os.Exit(1)
		}
		target := args[1]
		for _, d := range resp.Devices {
			if asString(d["name"]) == target || asString(d["hostname"]) == target {
				fmt.Printf("📍 %s\n", target)
				fmt.Printf("  Source:      %s\n", resp.Source)
				if resp.Source == "inventory-provider" {
					addr := asMap(d["addresses"])
					conn := asMap(d["connectivity"])
					fmt.Printf("  Hostname:    %s\n", asString(d["hostname"]))
					fmt.Printf("  Headscale:   %s\n", asString(addr["headscale"]))
					fmt.Printf("  Online:      %t\n", asBool(conn["online"]))
					fmt.Printf("  Type:        %s\n", asString(d["type"]))
					printInventorySyncStatus(resp.Sync)
				} else {
					fmt.Printf("  URL:         %s\n", asString(d["url"]))
					fmt.Printf("  Environment: %s\n", asString(d["environmentRef"]))
				}
				return
			}
		}
		fmt.Fprintf(os.Stderr, "Endpoint %q not found\n", target)
		os.Exit(1)
	}

	fmt.Println("📋 Infrastructure Inventory")
	fmt.Println("───────────────────────────")
	if resp.Source == "inventory-provider" {
		for _, d := range resp.Devices {
			name := asString(d["name"])
			addr := asMap(d["addresses"])
			conn := asMap(d["connectivity"])
			ip := asString(addr["headscale"])
			if ip == "" {
				ip = asString(addr["internal"])
			}
			state := "offline"
			if asBool(conn["online"]) {
				state = "online"
			}
			fmt.Printf("  %-25s %s  (%s)\n", name, ip, state)
		}
		fmt.Printf("\n%d devices (source: %s)\n", resp.Total, resp.Source)
		printInventorySyncStatus(resp.Sync)
		return
	}

	for _, d := range resp.Devices {
		fmt.Printf("  %-25s %s  (env: %s)\n", asString(d["name"]), asString(d["url"]), asString(d["environmentRef"]))
	}
	fmt.Printf("\n%d endpoints (source: %s)\n", resp.Total, resp.Source)
}

func printInventorySyncStatus(sync map[string]any) {
	if len(sync) == 0 {
		return
	}

	fmt.Println("\nSync status")
	fmt.Println("───────────")
	fmt.Printf("  Provider:   %s\n", asString(sync["provider"]))
	fmt.Printf("  Healthy:    %t\n", asBool(sync["healthy"]))
	fmt.Printf("  Stale:      %t\n", asBool(sync["stale"]))
	fmt.Printf("  Last sync:  %s\n", asString(sync["lastSuccess"]))
	if asString(sync["ageSinceLastSuccess"]) != "" {
		fmt.Printf("  Sync age:   %s\n", asString(sync["ageSinceLastSuccess"]))
	}
	if asString(sync["freshnessThreshold"]) != "" {
		fmt.Printf("  Freshness:  %s\n", asString(sync["freshnessThreshold"]))
	}
	if asString(sync["lastError"]) != "" {
		fmt.Printf("  Last error: %s\n", asString(sync["lastError"]))
	}
	if v := sync["consecutiveFailures"]; v != nil {
		fmt.Printf("  Failures:   %v\n", v)
	}
}

func inventoryShow(envs *unstructured.UnstructuredList, target string) {
	for _, env := range envs.Items {
		endpoints, found, _ := unstructured.NestedMap(env.Object, "spec", "endpoints")
		if !found {
			continue
		}

		if ep, ok := endpoints[target]; ok {
			epMap, _ := ep.(map[string]any)
			url, _ := epMap["url"].(string)

			fmt.Printf("📍 %s\n", target)
			fmt.Printf("  URL:         %s\n", url)
			fmt.Printf("  Environment: %s\n", env.GetName())

			// Check if there's a credential reference
			creds, found, _ := unstructured.NestedMap(env.Object, "spec", "credentials")
			if found {
				for name := range creds {
					fmt.Printf("  Credential:  %s\n", name)
				}
			}
			return
		}
	}

	fmt.Fprintf(os.Stderr, "Endpoint %q not found in any environment\n", target)
	os.Exit(1)
}
