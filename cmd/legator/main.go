/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// The `legator` CLI is a thin wrapper around the Kubernetes API for managing
// Legator agents, runs, and environments.
//
// Usage:
//
//	legator agents list             — list agents
//	legator agents get <name>       — show agent details
//	legator runs list [--agent X]   — list recent runs
//	legator runs logs <name>        — show run audit trail
//	legator status                  — cluster summary
//	legator version                 — version info
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

var (
	agentGVR = schema.GroupVersionResource{
		Group:    "legator.io",
		Version:  "v1alpha1",
		Resource: "legatoragents",
	}
	runGVR = schema.GroupVersionResource{
		Group:    "legator.io",
		Version:  "v1alpha1",
		Resource: "legatorruns",
	}
	envGVR = schema.GroupVersionResource{
		Group:    "legator.io",
		Version:  "v1alpha1",
		Resource: "legatorenvironments",
	}
	approvalGVR = schema.GroupVersionResource{
		Group:    "legator.io",
		Version:  "v1alpha1",
		Resource: "approvalrequests",
	}
)

const (
	approvalPhasePending  = "Pending"
	approvalPhaseApproved = "Approved"
	approvalPhaseDenied   = "Denied"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "agents", "agent":
		handleAgents(os.Args[2:])
	case "runs":
		handleRuns(os.Args[2:])
	case "run":
		handleRunAgent(os.Args[2:])
	case "check":
		handleCheck(os.Args[2:])
	case "login":
		handleLogin(os.Args[2:])
	case "logout":
		handleLogout(os.Args[2:])
	case "whoami", "me":
		handleWhoAmI(os.Args[2:])
	case "inventory", "inv":
		handleInventory(os.Args[2:])
	case "approvals", "approval":
		handleApprovals(os.Args[2:])
	case "approve":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: legator approve <name> [reason]")
			os.Exit(1)
		}
		reason := ""
		if len(os.Args) > 3 {
			reason = strings.Join(os.Args[3:], " ")
		}
		handleApprovalDecision(os.Args[2], approvalPhaseApproved, reason)
	case "deny":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: legator deny <name> [reason]")
			os.Exit(1)
		}
		reason := ""
		if len(os.Args) > 3 {
			reason = strings.Join(os.Args[3:], " ")
		}
		handleApprovalDecision(os.Args[2], approvalPhaseDenied, reason)
	case "skill", "skills":
		handleSkill(os.Args[2:])
	case "init":
		handleInit(os.Args[2:])
	case "validate":
		handleValidate(os.Args[2:])
	case "status":
		handleStatus()
	case "version":
		fmt.Printf("legator %s (commit: %s, built: %s)\n", version, gitCommit, buildDate)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`legator — Autonomous agent runtime for your IT estate

Usage:
  legator agents list               List all agents
  legator agents get <name>         Show agent details
  legator run <agent> [options]     Trigger an ad-hoc agent run
    --target <device>               Target device
    --task "description"            Task description
    --wait                          Wait for completion
  legator check <target>            Quick health check (via watchman-light)
  legator login [options]           OIDC device-code login for API access
    --issuer <url>                  OIDC issuer (default: env or dev-lab Keycloak)
    --client-id <id>                OIDC client ID (default: legator-cli)
    --api-url <url>                 Legator API URL to store with token
    --no-verify                     Skip immediate /api/v1/me verification
  legator whoami [--json]           Show authenticated identity + RBAC permissions
  legator logout                    Remove cached API login token
  legator inventory [--json]        List managed endpoints
  legator inventory show <name>      Show endpoint details
  legator inventory status [--json]  Show inventory sync health/freshness
  legator runs list [--agent X]     List recent runs
  legator runs logs <name>          Show run report/audit trail
  legator approvals                 List pending approvals
  legator approve <name> [reason]   Approve an action
  legator deny <name> [reason]      Deny an action
  legator skill pack <dir>          Package a skill directory
  legator skill push <dir> <ref>    Push skill to OCI registry
  legator skill pull <ref> [dir]    Pull skill from OCI registry
  legator skill inspect <dir>       Show skill manifest
  legator init [directory]          Create a new agent (interactive wizard)
  legator validate <directory>      Validate an agent directory
  legator status                    Cluster-wide summary
  legator version                   Show version info

Flags:
  -n, --namespace <ns>    Kubernetes namespace (default: all)

"One who delegates." — legator.io`)
}

func getClient() (dynamic.Interface, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	config := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, nil)

	restCfg, err := config.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	ns, _, _ := config.Namespace()
	if ns == "" {
		ns = "agents" // Legator default namespace
	}

	dc, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create client: %w", err)
	}

	return dc, ns, nil
}

func getNamespace(args []string) string {
	for i, arg := range args {
		if (arg == "-n" || arg == "--namespace") && i+1 < len(args) {
			return args[i+1]
		}
	}
	return "" // empty = use default
}

// --- Agents ---

func handleAgents(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: legator agents <list|get> [args]")
		os.Exit(1)
	}

	switch args[0] {
	case "list", "ls":
		agentsList(args[1:])
	case "get", "show":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: legator agents get <name>")
			os.Exit(1)
		}
		agentsGet(args[1], args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown agents subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func agentsList(args []string) {
	if apiClient, ok, err := tryAPIClient(); err != nil {
		fatal(err)
	} else if ok {
		agentsListViaAPI(apiClient)
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

	list, err := dc.Resource(agentGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	fatal(err)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tPHASE\tAUTONOMY\tSCHEDULE\tRUNS\tLAST RUN")

	for _, item := range list.Items {
		name := item.GetName()
		phase := getNestedString(item, "status", "phase")
		autonomy := getNestedString(item, "spec", "guardrails", "autonomy")
		schedule := getNestedString(item, "spec", "schedule", "cron")
		runs := getNestedInt(item, "status", "runCount")
		lastRun := getNestedString(item, "status", "lastRunTime")
		lastRunAgo := formatTimeAgo(lastRun)

		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
			name, phase, autonomy, schedule, runs, lastRunAgo)
	}
	_ = w.Flush()
}

func agentsGet(name string, args []string) {
	if apiClient, ok, err := tryAPIClient(); err != nil {
		fatal(err)
	} else if ok {
		agentsGetViaAPI(apiClient, name)
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

	agent, err := dc.Resource(agentGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	fatal(err)

	emoji := getNestedString(*agent, "spec", "emoji")
	desc := getNestedString(*agent, "spec", "description")
	phase := getNestedString(*agent, "status", "phase")
	autonomy := getNestedString(*agent, "spec", "guardrails", "autonomy")
	schedule := getNestedString(*agent, "spec", "schedule", "cron")
	runs := getNestedInt(*agent, "status", "runCount")
	lastRun := getNestedString(*agent, "status", "lastRunTime")
	envRef := getNestedString(*agent, "spec", "environmentRef")
	tier := getNestedString(*agent, "spec", "model", "tier")
	budget := getNestedInt(*agent, "spec", "model", "tokenBudget")
	maxIter := getNestedInt(*agent, "spec", "guardrails", "maxIterations")

	fmt.Printf("%s %s\n", emoji, name)
	fmt.Printf("  %s\n\n", strings.TrimSpace(desc))
	fmt.Printf("  Phase:        %s\n", phase)
	fmt.Printf("  Autonomy:     %s\n", autonomy)
	fmt.Printf("  Schedule:     %s\n", schedule)
	fmt.Printf("  Environment:  %s\n", envRef)
	fmt.Printf("  Model tier:   %s (budget: %d tokens)\n", tier, budget)
	fmt.Printf("  Max iters:    %d\n", maxIter)
	fmt.Printf("  Total runs:   %d\n", runs)
	fmt.Printf("  Last run:     %s\n", formatTimeAgo(lastRun))
}

func agentsListViaAPI(apiClient *legatorAPIClient) {
	var resp struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := apiClient.getJSON("/api/v1/agents", &resp); err != nil {
		fatal(err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tPHASE\tAUTONOMY\tSCHEDULE\tRUNS\tLAST RUN")
	for _, a := range resp.Agents {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			asString(a["name"]),
			asString(a["phase"]),
			asString(a["autonomy"]),
			asString(a["schedule"]),
			"-",
			"-",
		)
	}
	_ = w.Flush()
}

func agentsGetViaAPI(apiClient *legatorAPIClient, name string) {
	var agentObj map[string]any
	if err := apiClient.getJSON("/api/v1/agents/"+url.PathEscape(name), &agentObj); err != nil {
		fatal(err)
	}

	agent := unstructured.Unstructured{Object: agentObj}
	emoji := getNestedString(agent, "spec", "emoji")
	desc := getNestedString(agent, "spec", "description")
	phase := getNestedString(agent, "status", "phase")
	autonomy := getNestedString(agent, "spec", "guardrails", "autonomy")
	schedule := getNestedString(agent, "spec", "schedule", "cron")
	runs := getNestedInt(agent, "status", "runCount")
	lastRun := getNestedString(agent, "status", "lastRunTime")
	envRef := getNestedString(agent, "spec", "environmentRef")
	tier := getNestedString(agent, "spec", "model", "tier")
	budget := getNestedInt(agent, "spec", "model", "tokenBudget")
	maxIter := getNestedInt(agent, "spec", "guardrails", "maxIterations")

	fmt.Printf("%s %s\n", emoji, name)
	fmt.Printf("  %s\n\n", strings.TrimSpace(desc))
	fmt.Printf("  Phase:        %s\n", phase)
	fmt.Printf("  Autonomy:     %s\n", autonomy)
	fmt.Printf("  Schedule:     %s\n", schedule)
	fmt.Printf("  Environment:  %s\n", envRef)
	fmt.Printf("  Model tier:   %s (budget: %d tokens)\n", tier, budget)
	fmt.Printf("  Max iters:    %d\n", maxIter)
	fmt.Printf("  Total runs:   %d\n", runs)
	fmt.Printf("  Last run:     %s\n", formatTimeAgo(lastRun))
}

// --- Runs ---

func handleRuns(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: legator runs <list|logs> [args]")
		os.Exit(1)
	}

	switch args[0] {
	case "list", "ls":
		runsList(args[1:])
	case "logs", "log", "report":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: legator runs logs <name>")
			os.Exit(1)
		}
		runsLogs(args[1], args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown runs subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func runsList(args []string) {
	if apiClient, ok, err := tryAPIClient(); err != nil {
		fatal(err)
	} else if ok {
		runsListViaAPI(apiClient, args)
		return
	}

	dc, defaultNS, err := getClient()
	fatal(err)

	ns := getNamespace(args)
	if ns == "" {
		ns = defaultNS
	}

	// Parse --agent flag
	agentFilter := ""
	for i, arg := range args {
		if arg == "--agent" && i+1 < len(args) {
			agentFilter = args[i+1]
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	list, err := dc.Resource(runGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	fatal(err)

	// Sort by creation timestamp (newest first)
	sort.Slice(list.Items, func(i, j int) bool {
		ti := list.Items[i].GetCreationTimestamp()
		tj := list.Items[j].GetCreationTimestamp()
		return ti.After(tj.Time)
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tAGENT\tPHASE\tTRIGGER\tTOKENS\tDURATION\tAGE")

	count := 0
	for _, item := range list.Items {
		agent := getNestedString(item, "spec", "agentRef")
		if agentFilter != "" && agent != agentFilter {
			continue
		}

		name := item.GetName()
		phase := getNestedString(item, "status", "phase")
		trigger := getNestedString(item, "spec", "trigger")
		tokens := getNestedInt(item, "status", "usage", "totalTokens")
		wallMs := getNestedInt(item, "status", "usage", "wallClockMs")
		age := formatTimeAgo(item.GetCreationTimestamp().Format(time.RFC3339))

		duration := ""
		if wallMs > 0 {
			duration = formatDuration(time.Duration(wallMs) * time.Millisecond)
		}

		// Phase emoji
		phaseIcon := "⏳"
		switch phase {
		case "Succeeded":
			phaseIcon = "✅"
		case "Failed":
			phaseIcon = "❌"
		case "Running":
			phaseIcon = "🔄"
		}

		_, _ = fmt.Fprintf(w, "%s\t%s\t%s %s\t%s\t%d\t%s\t%s\n",
			name, agent, phaseIcon, phase, trigger, tokens, duration, age)

		count++
		if count >= 25 {
			break
		}
	}
	_ = w.Flush()

	if count == 0 && agentFilter != "" {
		fmt.Printf("\nNo runs found for agent %q\n", agentFilter)
	}
}

func runsLogs(name string, args []string) {
	if apiClient, ok, err := tryAPIClient(); err != nil {
		fatal(err)
	} else if ok {
		runsLogsViaAPI(apiClient, name)
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

	run, err := dc.Resource(runGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	fatal(err)

	agent := getNestedString(*run, "spec", "agentRef")
	phase := getNestedString(*run, "status", "phase")
	model := getNestedString(*run, "spec", "modelUsed")
	trigger := getNestedString(*run, "spec", "trigger")
	report := getNestedString(*run, "status", "report")
	tokens := getNestedInt(*run, "status", "usage", "totalTokens")
	iters := getNestedInt(*run, "status", "usage", "iterations")
	wallMs := getNestedInt(*run, "status", "usage", "wallClockMs")
	startTime := getNestedString(*run, "status", "startTime")
	endTime := getNestedString(*run, "status", "completionTime")

	fmt.Printf("Run: %s\n", name)
	fmt.Printf("Agent: %s | Phase: %s | Trigger: %s\n", agent, phase, trigger)
	fmt.Printf("Model: %s\n", model)
	fmt.Printf("Start: %s | End: %s\n", startTime, endTime)
	fmt.Printf("Iterations: %d | Tokens: %d | Duration: %s\n",
		iters, tokens, formatDuration(time.Duration(wallMs)*time.Millisecond))

	// Guardrails info
	autonomy := getNestedString(*run, "status", "guardrails", "autonomyCeiling")
	budgetIter := getNestedInt(*run, "status", "guardrails", "budgetUsed", "iterationsUsed")
	maxIter := getNestedInt(*run, "status", "guardrails", "budgetUsed", "maxIterations")
	tokenBudget := getNestedInt(*run, "status", "guardrails", "budgetUsed", "tokenBudget")
	fmt.Printf("Autonomy: %s | Budget: %d/%d iterations, %d token budget\n",
		autonomy, budgetIter, maxIter, tokenBudget)

	// Actions
	actions, found, _ := unstructured.NestedSlice(run.Object, "status", "actions")
	if found && len(actions) > 0 {
		fmt.Printf("\nActions (%d):\n", len(actions))
		for i, a := range actions {
			am, ok := a.(map[string]any)
			if !ok {
				continue
			}
			tool, _ := am["tool"].(string)
			status, _ := am["status"].(string)
			target, _ := am["target"].(string)

			statusIcon := "✅"
			if status == "blocked" {
				statusIcon = "🚫"
			} else if status == "skipped" {
				statusIcon = "⏭️"
			} else if status == "error" {
				statusIcon = "⚠️"
			}

			fmt.Printf("  %d. %s %s %s", i+1, statusIcon, tool, target)
			if status == "blocked" {
				reason, _ := am["blockReason"].(string)
				fmt.Printf(" — %s", reason)
			}
			fmt.Println()
		}
	}

	// Report
	if report != "" {
		fmt.Printf("\n--- Report ---\n%s\n", report)
	}

	// Error
	if phase == "Failed" {
		conditions, found, _ := unstructured.NestedSlice(run.Object, "status", "conditions")
		if found {
			for _, c := range conditions {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				msg, _ := cm["message"].(string)
				if msg != "" {
					fmt.Printf("\n--- Error ---\n%s\n", msg)
				}
			}
		}
	}
}

func runsListViaAPI(apiClient *legatorAPIClient, args []string) {
	agentFilter := ""
	for i, arg := range args {
		if arg == "--agent" && i+1 < len(args) {
			agentFilter = args[i+1]
		}
	}

	path := "/api/v1/runs"
	if agentFilter != "" {
		path += "?agent=" + url.QueryEscape(agentFilter)
	}

	var resp struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := apiClient.getJSON(path, &resp); err != nil {
		fatal(err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tAGENT\tPHASE\tTRIGGER\tTOKENS\tDURATION\tAGE")

	count := 0
	for _, run := range resp.Runs {
		phase := asString(run["phase"])
		phaseIcon := "⏳"
		switch phase {
		case "Succeeded":
			phaseIcon = "✅"
		case "Failed":
			phaseIcon = "❌"
		case "Running":
			phaseIcon = "🔄"
		}

		age := formatTimeAgo(asString(run["createdAt"]))
		duration := asString(run["duration"])
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s %s\t%s\t%s\t%s\t%s\n",
			asString(run["name"]),
			asString(run["agent"]),
			phaseIcon,
			phase,
			asString(run["trigger"]),
			"-",
			duration,
			age,
		)
		count++
		if count >= 25 {
			break
		}
	}
	_ = w.Flush()

	if count == 0 && agentFilter != "" {
		fmt.Printf("\nNo runs found for agent %q\n", agentFilter)
	}
}

func runsLogsViaAPI(apiClient *legatorAPIClient, name string) {
	var runObj map[string]any
	if err := apiClient.getJSON("/api/v1/runs/"+url.PathEscape(name), &runObj); err != nil {
		fatal(err)
	}

	run := unstructured.Unstructured{Object: runObj}
	agent := getNestedString(run, "spec", "agentRef")
	phase := getNestedString(run, "status", "phase")
	model := getNestedString(run, "spec", "modelUsed")
	trigger := getNestedString(run, "spec", "trigger")
	report := getNestedString(run, "status", "report")
	tokens := getNestedInt(run, "status", "usage", "totalTokens")
	iters := getNestedInt(run, "status", "usage", "iterations")
	wallMs := getNestedInt(run, "status", "usage", "wallClockMs")
	startTime := getNestedString(run, "status", "startTime")
	endTime := getNestedString(run, "status", "completionTime")

	fmt.Printf("Run: %s\n", name)
	fmt.Printf("Agent: %s | Phase: %s | Trigger: %s\n", agent, phase, trigger)
	fmt.Printf("Model: %s\n", model)
	fmt.Printf("Start: %s | End: %s\n", startTime, endTime)
	fmt.Printf("Iterations: %d | Tokens: %d | Duration: %s\n",
		iters, tokens, formatDuration(time.Duration(wallMs)*time.Millisecond))

	autonomy := getNestedString(run, "status", "guardrails", "autonomyCeiling")
	budgetIter := getNestedInt(run, "status", "guardrails", "budgetUsed", "iterationsUsed")
	maxIter := getNestedInt(run, "status", "guardrails", "budgetUsed", "maxIterations")
	tokenBudget := getNestedInt(run, "status", "guardrails", "budgetUsed", "tokenBudget")
	fmt.Printf("Autonomy: %s | Budget: %d/%d iterations, %d token budget\n",
		autonomy, budgetIter, maxIter, tokenBudget)

	actions, found, _ := unstructured.NestedSlice(run.Object, "status", "actions")
	if found && len(actions) > 0 {
		fmt.Printf("\nActions (%d):\n", len(actions))
		for i, a := range actions {
			am, ok := a.(map[string]any)
			if !ok {
				continue
			}
			tool, _ := am["tool"].(string)
			status, _ := am["status"].(string)
			target, _ := am["target"].(string)

			statusIcon := "✅"
			switch status {
			case "blocked":
				statusIcon = "🚫"
			case "skipped":
				statusIcon = "⏭️"
			case "error":
				statusIcon = "⚠️"
			}

			fmt.Printf("  %d. %s %s %s", i+1, statusIcon, tool, target)
			if status == "blocked" {
				reason, _ := am["blockReason"].(string)
				fmt.Printf(" — %s", reason)
			}
			fmt.Println()
		}
	}

	if report != "" {
		fmt.Printf("\n--- Report ---\n%s\n", report)
	}

	if phase == "Failed" {
		conditions, found, _ := unstructured.NestedSlice(run.Object, "status", "conditions")
		if found {
			for _, c := range conditions {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				msg, _ := cm["message"].(string)
				if msg != "" {
					fmt.Printf("\n--- Error ---\n%s\n", msg)
				}
			}
		}
	}
}

// --- Status ---

func handleStatus() {
	if apiClient, ok, err := tryAPIClient(); err != nil {
		fatal(err)
	} else if ok {
		handleStatusViaAPI(apiClient)
		return
	}

	dc, defaultNS, err := getClient()
	fatal(err)

	ns := getNamespace(os.Args[2:])
	if ns == "" {
		ns = defaultNS
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Count agents
	agents, err := dc.Resource(agentGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	fatal(err)

	// Count runs
	runs, err := dc.Resource(runGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	fatal(err)

	// Count environments
	envs, err := dc.Resource(envGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	fatal(err)

	// Analyse
	totalAgents := len(agents.Items)
	readyAgents := 0
	totalRuns := len(runs.Items)
	succeededRuns := 0
	failedRuns := 0
	runningRuns := 0
	var totalTokens int64

	for _, a := range agents.Items {
		if getNestedString(a, "status", "phase") == "Ready" {
			readyAgents++
		}
	}

	for _, r := range runs.Items {
		phase := getNestedString(r, "status", "phase")
		switch phase {
		case "Succeeded":
			succeededRuns++
		case "Failed":
			failedRuns++
		case "Running":
			runningRuns++
		}
		totalTokens += int64(getNestedInt(r, "status", "usage", "totalTokens"))
	}

	successRate := 0.0
	completed := succeededRuns + failedRuns
	if completed > 0 {
		successRate = float64(succeededRuns) / float64(completed) * 100
	}

	fmt.Println("⚡ Legator Status")
	fmt.Println("─────────────────")
	fmt.Printf("Agents:       %d/%d ready\n", readyAgents, totalAgents)
	fmt.Printf("Environments: %d\n", len(envs.Items))
	fmt.Printf("Runs:         %d total (%d succeeded, %d failed, %d running)\n",
		totalRuns, succeededRuns, failedRuns, runningRuns)
	fmt.Printf("Success rate: %.1f%%\n", successRate)
	fmt.Printf("Tokens used:  %s\n", formatTokens(totalTokens))
	fmt.Printf("Namespace:    %s\n", ns)
}

func handleStatusViaAPI(apiClient *legatorAPIClient) {
	var agentsResp struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := apiClient.getJSON("/api/v1/agents", &agentsResp); err != nil {
		fatal(err)
	}

	var runsResp struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := apiClient.getJSON("/api/v1/runs", &runsResp); err != nil {
		fatal(err)
	}

	var invResp struct {
		Devices []map[string]any `json:"devices"`
		Source  string           `json:"source"`
	}
	_ = apiClient.getJSON("/api/v1/inventory", &invResp)

	totalAgents := len(agentsResp.Agents)
	readyAgents := 0
	for _, a := range agentsResp.Agents {
		if asString(a["phase"]) == "Ready" {
			readyAgents++
		}
	}

	totalRuns := len(runsResp.Runs)
	succeededRuns := 0
	failedRuns := 0
	runningRuns := 0
	for _, r := range runsResp.Runs {
		switch asString(r["phase"]) {
		case "Succeeded":
			succeededRuns++
		case "Failed":
			failedRuns++
		case "Running":
			runningRuns++
		}
	}

	envCount := 0
	if invResp.Source == "environment-endpoints" {
		seen := map[string]bool{}
		for _, d := range invResp.Devices {
			env := asString(d["environmentRef"])
			if env != "" {
				seen[env] = true
			}
		}
		envCount = len(seen)
	}

	successRate := 0.0
	completed := succeededRuns + failedRuns
	if completed > 0 {
		successRate = float64(succeededRuns) / float64(completed) * 100
	}

	fmt.Println("⚡ Legator Status")
	fmt.Println("─────────────────")
	fmt.Printf("Agents:       %d/%d ready\n", readyAgents, totalAgents)
	if envCount > 0 {
		fmt.Printf("Environments: %d\n", envCount)
	} else {
		fmt.Printf("Environments: n/a (API mode)\n")
	}
	fmt.Printf("Runs:         %d total (%d succeeded, %d failed, %d running)\n",
		totalRuns, succeededRuns, failedRuns, runningRuns)
	fmt.Printf("Success rate: %.1f%%\n", successRate)
	fmt.Printf("Tokens used:  n/a (run summary API)\n")
	fmt.Printf("Source:       API (%s)\n", apiClient.baseURL)
}

// --- Helpers ---

func fatal(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func getNestedString(item unstructured.Unstructured, fields ...string) string {
	val, found, err := unstructured.NestedString(item.Object, fields...)
	if err != nil || !found {
		return ""
	}
	return val
}

func getNestedInt(item unstructured.Unstructured, fields ...string) int {
	// Try int64 first
	val, found, err := unstructured.NestedInt64(item.Object, fields...)
	if err == nil && found {
		return int(val)
	}
	// Try float64 (JSON numbers)
	fval, found, err := unstructured.NestedFloat64(item.Object, fields...)
	if err == nil && found {
		return int(fval)
	}
	return 0
}

func formatTimeAgo(timeStr string) string {
	if timeStr == "" {
		return "never"
	}
	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return timeStr
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}

func formatTokens(tokens int64) string {
	if tokens >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	}
	if tokens >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1_000)
	}
	return fmt.Sprintf("%d", tokens)
}

// --- Approval commands ---

func handleApprovals(args []string) {
	if apiClient, ok, err := tryAPIClient(); err != nil {
		fatal(err)
	} else if ok {
		handleApprovalsViaAPI(apiClient, args)
		return
	}

	client, ns, err := getClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Parse namespace flag
	namespace := ns
	for i, arg := range args {
		if (arg == "-n" || arg == "--namespace") && i+1 < len(args) {
			namespace = args[i+1]
			args = append(args[:i], args[i+2:]...)
			break
		}
	}

	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "list", "ls":
		listApprovals(client, namespace)
	default:
		fmt.Fprintf(os.Stderr, "Unknown approvals subcommand: %s\n", sub)
		os.Exit(1)
	}
}

func handleApprovalsViaAPI(apiClient *legatorAPIClient, args []string) {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	if sub != "list" && sub != "ls" {
		fmt.Fprintf(os.Stderr, "Unknown approvals subcommand: %s\n", sub)
		os.Exit(1)
	}

	var resp struct {
		Approvals []map[string]any `json:"approvals"`
	}
	if err := apiClient.getJSON("/api/v1/approvals", &resp); err != nil {
		fatal(err)
	}
	if len(resp.Approvals) == 0 {
		fmt.Println("No approval requests found.")
		return
	}

	sort.Slice(resp.Approvals, func(i, j int) bool {
		ai := unstructured.Unstructured{Object: resp.Approvals[i]}
		aj := unstructured.Unstructured{Object: resp.Approvals[j]}
		pi := getNestedString(ai, "status", "phase")
		pj := getNestedString(aj, "status", "phase")
		if pi == approvalPhasePending && pj != approvalPhasePending {
			return true
		}
		if pi != approvalPhasePending && pj == approvalPhasePending {
			return false
		}
		return ai.GetCreationTimestamp().After(aj.GetCreationTimestamp().Time)
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "STATUS\tNAME\tAGENT\tTOOL\tTIER\tAGE")
	for _, obj := range resp.Approvals {
		item := unstructured.Unstructured{Object: obj}
		phase := getNestedString(item, "status", "phase")
		if phase == "" {
			phase = approvalPhasePending
		}
		agent := getNestedString(item, "spec", "agentName")
		tool := getNestedString(item, "spec", "action", "tool")
		tier := getNestedString(item, "spec", "action", "tier")
		age := formatTimeAgo(item.GetCreationTimestamp().Format(time.RFC3339))

		icon := "❓"
		switch phase {
		case approvalPhasePending:
			icon = "⏳"
		case approvalPhaseApproved:
			icon = "✅"
		case approvalPhaseDenied:
			icon = "❌"
		case "Expired":
			icon = "⏰"
		}
		_, _ = fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\t%s\t%s\n", icon, phase, item.GetName(), agent, tool, tier, age)
	}
	_ = w.Flush()
}

func listApprovals(client dynamic.Interface, namespace string) {
	ctx := context.Background()

	var list *unstructured.UnstructuredList
	var err error
	if namespace != "" {
		list, err = client.Resource(approvalGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	} else {
		list, err = client.Resource(approvalGVR).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing approvals: %v\n", err)
		os.Exit(1)
	}

	if len(list.Items) == 0 {
		fmt.Println("No approval requests found.")
		return
	}

	// Sort: pending first, then by creation time
	sort.Slice(list.Items, func(i, j int) bool {
		pi := getNestedString(list.Items[i], "status", "phase")
		pj := getNestedString(list.Items[j], "status", "phase")
		if pi == approvalPhasePending && pj != approvalPhasePending {
			return true
		}
		if pi != approvalPhasePending && pj == approvalPhasePending {
			return false
		}
		ti := list.Items[i].GetCreationTimestamp()
		tj := list.Items[j].GetCreationTimestamp()
		return ti.After(tj.Time)
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "STATUS\tNAME\tAGENT\tTOOL\tTIER\tAGE")

	for _, item := range list.Items {
		phase := getNestedString(item, "status", "phase")
		if phase == "" {
			phase = approvalPhasePending
		}
		agent := getNestedString(item, "spec", "agentName")
		tool := getNestedString(item, "spec", "action", "tool")
		tier := getNestedString(item, "spec", "action", "tier")
		age := formatTimeAgo(item.GetCreationTimestamp().Format(time.RFC3339))

		icon := "❓"
		switch phase {
		case approvalPhasePending:
			icon = "⏳"
		case approvalPhaseApproved:
			icon = "✅"
		case approvalPhaseDenied:
			icon = "❌"
		case "Expired":
			icon = "⏰"
		}

		_, _ = fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\t%s\t%s\n", icon, phase, item.GetName(), agent, tool, tier, age)
	}
	_ = w.Flush()
}

func handleApprovalDecisionViaAPI(apiClient *legatorAPIClient, name, decision, reason string) {
	apiDecision := "approve"
	if strings.EqualFold(decision, approvalPhaseDenied) {
		apiDecision = "deny"
	}

	payload := map[string]any{"decision": apiDecision}
	if reason != "" {
		payload["reason"] = reason
	}
	if err := apiClient.postJSON("/api/v1/approvals/"+url.PathEscape(name), payload, nil); err != nil {
		fatal(err)
	}

	icon := "✅"
	pretty := approvalPhaseApproved
	if apiDecision == "deny" {
		icon = "❌"
		pretty = approvalPhaseDenied
	}
	fmt.Printf("%s %s: %s", icon, pretty, name)
	if reason != "" {
		fmt.Printf(" (%s)", reason)
	}
	fmt.Println()
}

func handleApprovalDecision(name, decision, reason string) {
	if apiClient, ok, err := tryAPIClient(); err != nil {
		fatal(err)
	} else if ok {
		handleApprovalDecisionViaAPI(apiClient, name, decision, reason)
		return
	}

	client, ns, err := getClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	namespace := ns
	if namespace == "" {
		namespace = "agents" // sensible default
	}

	ctx := context.Background()

	// Get the approval request
	ar, err := client.Resource(approvalGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		// Try all namespaces
		list, listErr := client.Resource(approvalGVR).List(ctx, metav1.ListOptions{})
		if listErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		for _, item := range list.Items {
			if item.GetName() == name {
				ar = &item
				namespace = item.GetNamespace()
				break
			}
		}
		if ar == nil {
			fmt.Fprintf(os.Stderr, "ApprovalRequest %q not found\n", name)
			os.Exit(1)
		}
	}

	// Check it's still pending
	currentPhase := getNestedString(*ar, "status", "phase")
	if currentPhase != "" && currentPhase != approvalPhasePending {
		fmt.Fprintf(os.Stderr, "ApprovalRequest %q is already %s\n", name, currentPhase)
		os.Exit(1)
	}

	// Update status
	status := map[string]any{
		"phase":     decision,
		"decidedBy": "legator-cli",
		"decidedAt": time.Now().UTC().Format(time.RFC3339),
	}
	if reason != "" {
		status["reason"] = reason
	}

	ar.Object["status"] = status

	_, err = client.Resource(approvalGVR).Namespace(namespace).UpdateStatus(ctx, ar, metav1.UpdateOptions{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating approval: %v\n", err)
		os.Exit(1)
	}

	icon := "✅"
	if decision == approvalPhaseDenied {
		icon = "❌"
	}

	agent := getNestedString(*ar, "spec", "agentName")
	tool := getNestedString(*ar, "spec", "action", "tool")
	target := getNestedString(*ar, "spec", "action", "target")

	fmt.Printf("%s %s: %s → %s %s", icon, decision, agent, tool, target)
	if reason != "" {
		fmt.Printf(" (%s)", reason)
	}
	fmt.Println()
}
