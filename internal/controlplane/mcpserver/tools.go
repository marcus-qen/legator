package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listProbesInput struct {
	Status string `json:"status,omitempty" jsonschema:"probe status filter: online, offline, or all"`
	Tag    string `json:"tag,omitempty" jsonschema:"optional tag filter"`
}

type probeInfoInput struct {
	ProbeID string `json:"probe_id" jsonschema:"probe identifier"`
}

type runCommandInput struct {
	ProbeID string `json:"probe_id" jsonschema:"probe identifier"`
	Command string `json:"command" jsonschema:"shell command to run"`
}

type fleetQueryInput struct {
	Question string `json:"question" jsonschema:"natural language fleet question"`
}

type searchAuditInput struct {
	ProbeID string `json:"probe_id,omitempty" jsonschema:"optional probe id filter"`
	Type    string `json:"type,omitempty" jsonschema:"optional audit event type filter"`
	Since   string `json:"since,omitempty" jsonschema:"optional ISO-8601 timestamp filter"`
	Limit   int    `json:"limit,omitempty" jsonschema:"optional limit (default 50)"`
}

type probeSummary struct {
	ID       string    `json:"id"`
	Hostname string    `json:"hostname"`
	Status   string    `json:"status"`
	Tags     []string  `json:"tags,omitempty"`
	LastSeen time.Time `json:"last_seen"`
}

func (s *MCPServer) registerTools() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_list_probes",
		Description: "List probes in the Legator fleet with status/tag filtering",
	}, s.handleListProbes)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_probe_info",
		Description: "Get detailed state for a specific probe",
	}, s.handleProbeInfo)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_run_command",
		Description: "Run a command on a probe and wait for the result",
	}, s.handleRunCommand)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_get_inventory",
		Description: "Get system inventory for a specific probe",
	}, s.handleGetInventory)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_fleet_query",
		Description: "Answer a natural-language fleet query using summary stats",
	}, s.handleFleetQuery)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_search_audit",
		Description: "Search Legator audit events",
	}, s.handleSearchAudit)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_probe_health",
		Description: "Get health score/status/warnings for a probe",
	}, s.handleProbeHealth)
}

func (s *MCPServer) handleListProbes(_ context.Context, _ *mcp.CallToolRequest, input listProbesInput) (*mcp.CallToolResult, any, error) {
	if s.fleetStore == nil {
		return nil, nil, fmt.Errorf("fleet store unavailable")
	}

	status := strings.ToLower(strings.TrimSpace(input.Status))
	if status == "" {
		status = "all"
	}
	if status != "all" && status != "online" && status != "offline" {
		return nil, nil, fmt.Errorf("invalid status %q: expected online, offline, or all", input.Status)
	}

	var probes []*fleet.ProbeState
	tag := strings.TrimSpace(input.Tag)
	if tag != "" {
		probes = s.fleetStore.ListByTag(tag)
	} else {
		probes = s.fleetStore.List()
	}

	out := make([]probeSummary, 0, len(probes))
	for _, ps := range probes {
		if status != "all" && strings.ToLower(ps.Status) != status {
			continue
		}
		out = append(out, probeSummary{
			ID:       ps.ID,
			Hostname: ps.Hostname,
			Status:   ps.Status,
			Tags:     append([]string(nil), ps.Tags...),
			LastSeen: ps.LastSeen,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})

	return jsonToolResult(out)
}

func (s *MCPServer) handleProbeInfo(_ context.Context, _ *mcp.CallToolRequest, input probeInfoInput) (*mcp.CallToolResult, any, error) {
	if s.fleetStore == nil {
		return nil, nil, fmt.Errorf("fleet store unavailable")
	}
	probeID := strings.TrimSpace(input.ProbeID)
	if probeID == "" {
		return nil, nil, fmt.Errorf("probe_id is required")
	}

	ps, ok := s.fleetStore.Get(probeID)
	if !ok {
		return nil, nil, fmt.Errorf("probe not found: %s", probeID)
	}

	return jsonToolResult(ps)
}

func (s *MCPServer) handleRunCommand(ctx context.Context, _ *mcp.CallToolRequest, input runCommandInput) (*mcp.CallToolResult, any, error) {
	if s.fleetStore == nil {
		return nil, nil, fmt.Errorf("fleet store unavailable")
	}
	if s.dispatcher == nil {
		return nil, nil, fmt.Errorf("command transport unavailable")
	}

	probeID := strings.TrimSpace(input.ProbeID)
	if probeID == "" {
		return nil, nil, fmt.Errorf("probe_id is required")
	}
	command := strings.TrimSpace(input.Command)
	if command == "" {
		return nil, nil, fmt.Errorf("command is required")
	}

	ps, ok := s.fleetStore.Get(probeID)
	if !ok {
		return nil, nil, fmt.Errorf("probe not found: %s", probeID)
	}

	cmd := protocol.CommandPayload{
		RequestID: fmt.Sprintf("cmd-%d", time.Now().UnixNano()%100000),
		Command:   command,
		Level:     ps.PolicyLevel,
	}

	envelope := s.dispatcher.DispatchWithPolicy(ctx, probeID, cmd, corecommanddispatch.WaitPolicy(30*time.Second))
	if envelope == nil {
		return nil, nil, fmt.Errorf("command failed: empty result")
	}
	if err := envelope.MCPError(); err != nil {
		return nil, nil, err
	}
	if envelope.Result == nil {
		return nil, nil, fmt.Errorf("command failed: empty result")
	}

	return textToolResult(corecommanddispatch.ResultText(envelope.Result)), nil, nil
}

func (s *MCPServer) handleGetInventory(_ context.Context, _ *mcp.CallToolRequest, input probeInfoInput) (*mcp.CallToolResult, any, error) {
	if s.fleetStore == nil {
		return nil, nil, fmt.Errorf("fleet store unavailable")
	}
	probeID := strings.TrimSpace(input.ProbeID)
	if probeID == "" {
		return nil, nil, fmt.Errorf("probe_id is required")
	}

	ps, ok := s.fleetStore.Get(probeID)
	if !ok {
		return nil, nil, fmt.Errorf("probe not found: %s", probeID)
	}

	return jsonToolResult(ps.Inventory)
}

func (s *MCPServer) handleFleetQuery(_ context.Context, _ *mcp.CallToolRequest, input fleetQueryInput) (*mcp.CallToolResult, any, error) {
	if s.fleetStore == nil {
		return nil, nil, fmt.Errorf("fleet store unavailable")
	}
	question := strings.TrimSpace(input.Question)
	if question == "" {
		return nil, nil, fmt.Errorf("question is required")
	}

	probes := s.fleetStore.List()
	counts := s.fleetStore.Count()
	tags := s.fleetStore.TagCounts()
	inventory := s.fleetStore.Inventory(fleet.InventoryFilter{})

	tagPairs := make([]string, 0, len(tags))
	for tag, count := range tags {
		tagPairs = append(tagPairs, fmt.Sprintf("%s=%d", tag, count))
	}
	sort.Strings(tagPairs)
	if len(tagPairs) == 0 {
		tagPairs = append(tagPairs, "none")
	}

	text := fmt.Sprintf(
		"Fleet summary for query %q\nTotal probes: %d\nOnline: %d\nOffline: %d\nDegraded: %d\nTag summary: %s\nInventory totals: CPUs=%d RAM=%d bytes",
		question,
		len(probes),
		counts["online"],
		counts["offline"],
		counts["degraded"],
		strings.Join(tagPairs, ", "),
		inventory.Aggregates.TotalCPUs,
		inventory.Aggregates.TotalRAMBytes,
	)

	return textToolResult(text), nil, nil
}

func (s *MCPServer) handleSearchAudit(_ context.Context, _ *mcp.CallToolRequest, input searchAuditInput) (*mcp.CallToolResult, any, error) {
	if s.auditStore == nil {
		return nil, nil, fmt.Errorf("audit store unavailable")
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 50
	}

	filter := audit.Filter{
		ProbeID: strings.TrimSpace(input.ProbeID),
		Type:    audit.EventType(strings.TrimSpace(input.Type)),
		Limit:   limit,
	}

	if sinceRaw := strings.TrimSpace(input.Since); sinceRaw != "" {
		since, err := time.Parse(time.RFC3339, sinceRaw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid since timestamp (expected RFC3339): %w", err)
		}
		filter.Since = since
	}

	events := s.auditStore.Query(filter)
	return jsonToolResult(events)
}

func (s *MCPServer) handleProbeHealth(_ context.Context, _ *mcp.CallToolRequest, input probeInfoInput) (*mcp.CallToolResult, any, error) {
	if s.fleetStore == nil {
		return nil, nil, fmt.Errorf("fleet store unavailable")
	}
	probeID := strings.TrimSpace(input.ProbeID)
	if probeID == "" {
		return nil, nil, fmt.Errorf("probe_id is required")
	}

	ps, ok := s.fleetStore.Get(probeID)
	if !ok {
		return nil, nil, fmt.Errorf("probe not found: %s", probeID)
	}

	health := fleet.HealthScore{Score: 0, Status: "unknown", Warnings: []string{"no health data"}}
	if ps.Health != nil {
		health = *ps.Health
	}
	return jsonToolResult(health)
}

func jsonToolResult(v any) (*mcp.CallToolResult, any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return textToolResult(string(data)), nil, nil
}

func textToolResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}
