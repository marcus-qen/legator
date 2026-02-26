package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

const fleetChatSystemPrompt = `You are Legator, an AI infrastructure assistant operating across a fleet of remote probes.

Your capabilities:
1. Answer fleet-level questions using inventory context from all online probes.
2. Run shell commands on one or more probes to gather evidence.
3. Summarize results clearly for an experienced operator.

IMPORTANT: Each response must be EITHER a command OR conversational text. Never mix them.

To run a command, your ENTIRE response must be JSON and include a target:
{"command":"df -h /", "probe":"prb-51fd13f7", "reason":"check disk"}
{"command":"uptime", "target":"all", "reason":"fleet uptime"}
{"command":"free -m", "target":"tag:k8s-host", "reason":"memory check"}

To reply conversationally (no command needed), write plain text only.

Rules:
- Be concise and practical.
- Respect each probe's policy level.
- Never run destructive commands without explicit confirmation.
- After command results return, summarize clearly and suggest next checks only when useful.`

// FleetCommandRequest extends command requests with probe targeting.
type FleetCommandRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Reason  string   `json:"reason"`
	Probe   string   `json:"probe,omitempty"`
	Target  string   `json:"target,omitempty"`
}

// FleetChatResponder generates LLM-backed replies for fleet chat sessions.
type FleetChatResponder struct {
	provider Provider
	fleet    fleet.Fleet
	dispatch CommandDispatcher
	logger   *zap.Logger
	maxSteps int
}

// NewFleetChatResponder creates a fleet chat responder wired to LLM + fleet manager + command dispatch.
func NewFleetChatResponder(provider Provider, fleetMgr fleet.Fleet, dispatch CommandDispatcher, logger *zap.Logger) *FleetChatResponder {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &FleetChatResponder{
		provider: provider,
		fleet:    fleetMgr,
		dispatch: dispatch,
		logger:   logger,
		maxSteps: 5,
	}
}

// Respond generates a fleet-aware chat response.
func (fr *FleetChatResponder) Respond(ctx context.Context, history []ChatMessage, userMsg string) (string, error) {
	inventory := fr.fleet.Inventory(fleet.InventoryFilter{Status: "online"})
	fleetCtx := buildFleetContext(inventory)

	messages := []Message{{
		Role:    RoleSystem,
		Content: fleetChatSystemPrompt + "\n\n[Fleet Context]\n" + fleetCtx,
	}}

	histStart := 0
	if len(history) > 20 {
		histStart = len(history) - 20
	}
	for _, msg := range history[histStart:] {
		if msg.Role == RoleSystem {
			continue
		}
		messages = append(messages, Message(msg))
	}
	messages = append(messages, Message{Role: RoleUser, Content: userMsg})

	for step := 0; step < fr.maxSteps; step++ {
		resp, err := fr.provider.Complete(ctx, &CompletionRequest{
			Messages:    messages,
			Temperature: 0.2,
			MaxTokens:   1200,
		})
		if err != nil {
			return "", fmt.Errorf("LLM error: %w", err)
		}

		content := strings.TrimSpace(resp.Content)
		messages = append(messages, Message{Role: RoleAssistant, Content: content})

		cmdReq, found := extractFleetCommand(content)
		if !found {
			return content, nil
		}

		targets, err := fr.resolveTargets(cmdReq, inventory.Probes)
		if err != nil {
			messages = append(messages, Message{
				Role:    RoleUser,
				Content: fmt.Sprintf("[Command target error] %s", err.Error()),
			})
			continue
		}

		feedback := fr.dispatchFleetCommand(cmdReq, targets, step)
		messages = append(messages, Message{Role: RoleUser, Content: feedback})
	}

	return "I reached the maximum number of command iterations for this message. Please send another message to continue.", nil
}

func buildFleetContext(inv fleet.FleetInventory) string {
	if len(inv.Probes) == 0 {
		return "No online probes currently available."
	}

	lines := []string{
		fmt.Sprintf("Online probes: %d", inv.Aggregates.Online),
		fmt.Sprintf("Total CPUs: %d", inv.Aggregates.TotalCPUs),
		fmt.Sprintf("Total RAM: %d MB", inv.Aggregates.TotalRAMBytes/(1024*1024)),
		"",
		"Probe summaries:",
	}

	for _, probe := range inv.Probes {
		line := fmt.Sprintf("- %s (%s) status=%s os=%s/%s policy=%s cpus=%d ram=%dMB disk=%dGB",
			probe.ID,
			hostnameOrID(probe.Hostname, probe.ID),
			probe.Status,
			probe.OS,
			probe.Arch,
			probe.PolicyLevel,
			probe.CPUs,
			probe.RAMBytes/(1024*1024),
			probe.DiskBytes/(1024*1024*1024),
		)
		if len(probe.Tags) > 0 {
			line += " tags=" + strings.Join(probe.Tags, ",")
		}
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func hostnameOrID(hostname, id string) string {
	if strings.TrimSpace(hostname) != "" {
		return hostname
	}
	return id
}

func (fr *FleetChatResponder) resolveTargets(req FleetCommandRequest, probes []fleet.ProbeInventorySummary) ([]fleet.ProbeInventorySummary, error) {
	if strings.TrimSpace(req.Command) == "" {
		return nil, fmt.Errorf("command is required")
	}

	if strings.TrimSpace(req.Probe) != "" {
		for _, probe := range probes {
			if probe.ID == req.Probe {
				return []fleet.ProbeInventorySummary{probe}, nil
			}
		}
		return nil, fmt.Errorf("probe %q is not online or does not exist", req.Probe)
	}

	target := strings.TrimSpace(req.Target)
	if target == "" {
		return nil, fmt.Errorf("missing command target (set probe or target)")
	}

	if strings.EqualFold(target, "all") {
		if len(probes) == 0 {
			return nil, fmt.Errorf("no online probes available")
		}
		return append([]fleet.ProbeInventorySummary(nil), probes...), nil
	}

	if strings.HasPrefix(strings.ToLower(target), "tag:") {
		tag := strings.TrimSpace(target[4:])
		if tag == "" {
			return nil, fmt.Errorf("missing tag value in target")
		}
		matches := make([]fleet.ProbeInventorySummary, 0, len(probes))
		for _, probe := range probes {
			for _, probeTag := range probe.Tags {
				if strings.EqualFold(probeTag, tag) {
					matches = append(matches, probe)
					break
				}
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("no online probes match tag %q", tag)
		}
		return matches, nil
	}

	for _, probe := range probes {
		if probe.ID == target {
			return []fleet.ProbeInventorySummary{probe}, nil
		}
	}

	return nil, fmt.Errorf("unknown target %q", target)
}

func (fr *FleetChatResponder) dispatchFleetCommand(req FleetCommandRequest, targets []fleet.ProbeInventorySummary, step int) string {
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })

	lines := []string{fmt.Sprintf("[Fleet command result] command=%q reason=%q", req.Command, req.Reason)}
	for idx, target := range targets {
		payload := &protocol.CommandPayload{
			RequestID: fmt.Sprintf("fleet-chat-%d-%d-%d", time.Now().UnixNano()%100000, step, idx),
			Command:   req.Command,
			Args:      req.Args,
			Level:     target.PolicyLevel,
			Timeout:   30 * time.Second,
		}

		cmdResult, err := fr.dispatch(target.ID, payload)
		if err != nil {
			lines = append(lines, fmt.Sprintf("- %s dispatch_error=%s", target.ID, err.Error()))
			continue
		}

		stdout := truncate(cmdResult.Stdout, 2000)
		stderr := truncate(cmdResult.Stderr, 800)
		line := fmt.Sprintf("- %s exit=%d duration=%dms", target.ID, cmdResult.ExitCode, cmdResult.Duration)
		if stdout != "" {
			line += "\n  stdout:\n" + indentLines(stdout, "    ")
		}
		if stderr != "" {
			line += "\n  stderr:\n" + indentLines(stderr, "    ")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func indentLines(in, prefix string) string {
	if in == "" {
		return ""
	}
	parts := strings.Split(in, "\n")
	for i := range parts {
		parts[i] = prefix + parts[i]
	}
	return strings.Join(parts, "\n")
}

// extractFleetCommand tries to parse a fleet command from response text.
func extractFleetCommand(content string) (FleetCommandRequest, bool) {
	var req FleetCommandRequest
	if err := json.Unmarshal([]byte(content), &req); err == nil && strings.TrimSpace(req.Command) != "" {
		return req, true
	}

	start := strings.Index(content, `{"command"`)
	if start == -1 {
		return FleetCommandRequest{}, false
	}

	depth := 0
	for i := start; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				candidate := content[start : i+1]
				if err := json.Unmarshal([]byte(candidate), &req); err == nil && strings.TrimSpace(req.Command) != "" {
					return req, true
				}
				return FleetCommandRequest{}, false
			}
		}
	}

	return FleetCommandRequest{}, false
}
