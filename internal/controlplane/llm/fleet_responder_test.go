package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

type fleetMockProvider struct {
	responses []string
	requests  []*CompletionRequest
	idx       int
}

func (m *fleetMockProvider) Name() string { return "mock" }

func (m *fleetMockProvider) Complete(_ context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	copied := &CompletionRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Messages:    append([]Message(nil), req.Messages...),
	}
	m.requests = append(m.requests, copied)

	if m.idx >= len(m.responses) {
		return &CompletionResponse{Content: "done"}, nil
	}
	resp := m.responses[m.idx]
	m.idx++
	return &CompletionResponse{Content: resp}, nil
}

func TestBuildFleetContext(t *testing.T) {
	ctx := buildFleetContext(fleet.FleetInventory{
		Probes: []fleet.ProbeInventorySummary{
			{ID: "probe-1", Hostname: "web-01", Status: "online", OS: "linux", Arch: "amd64", PolicyLevel: protocol.CapObserve, CPUs: 4, RAMBytes: 8 * 1024 * 1024 * 1024, Tags: []string{"prod"}},
			{ID: "probe-2", Hostname: "db-01", Status: "online", OS: "linux", Arch: "arm64", PolicyLevel: protocol.CapDiagnose, CPUs: 8, RAMBytes: 16 * 1024 * 1024 * 1024, Tags: []string{"db"}},
		},
		Aggregates: fleet.FleetAggregates{Online: 2, TotalCPUs: 12, TotalRAMBytes: 24 * 1024 * 1024 * 1024},
	})

	for _, snippet := range []string{"Online probes: 2", "Total CPUs: 12", "probe-1", "probe-2", "tags=prod", "tags=db"} {
		if !strings.Contains(ctx, snippet) {
			t.Fatalf("context missing %q: %s", snippet, ctx)
		}
	}
}

func TestExtractFleetCommandAndTargetResolution(t *testing.T) {
	responder := &FleetChatResponder{}
	probes := []fleet.ProbeInventorySummary{
		{ID: "p1", Tags: []string{"k8s-host", "prod"}},
		{ID: "p2", Tags: []string{"prod"}},
	}

	req, found := extractFleetCommand(`{"command":"uptime","target":"all","reason":"fleet uptime"}`)
	if !found || req.Command != "uptime" || req.Target != "all" {
		t.Fatalf("failed to parse all target command: %#v found=%v", req, found)
	}
	all, err := responder.resolveTargets(req, probes)
	if err != nil || len(all) != 2 {
		t.Fatalf("expected all targets, got len=%d err=%v", len(all), err)
	}

	req, found = extractFleetCommand(`prefix {"command":"free -m","target":"tag:k8s-host","reason":"memory"} suffix`)
	if !found {
		t.Fatal("expected embedded command to parse")
	}
	byTag, err := responder.resolveTargets(req, probes)
	if err != nil || len(byTag) != 1 || byTag[0].ID != "p1" {
		t.Fatalf("expected tag target p1, got %#v err=%v", byTag, err)
	}

	req, found = extractFleetCommand(`{"command":"df -h /","probe":"p2","reason":"disk"}`)
	if !found {
		t.Fatal("expected probe command to parse")
	}
	one, err := responder.resolveTargets(req, probes)
	if err != nil || len(one) != 1 || one[0].ID != "p2" {
		t.Fatalf("expected probe target p2, got %#v err=%v", one, err)
	}
}

func TestFleetChatResponder_MultiProbeDispatch(t *testing.T) {
	mgr := fleet.NewManager(zap.NewNop())
	mgr.Register("probe-1", "web-01", "linux", "amd64")
	mgr.Register("probe-2", "db-01", "linux", "amd64")
	_ = mgr.SetTags("probe-1", []string{"prod"})
	_ = mgr.SetTags("probe-2", []string{"prod"})
	_ = mgr.UpdateInventory("probe-1", &protocol.InventoryPayload{CPUs: 4, MemTotal: 8 * 1024 * 1024 * 1024, OS: "linux", Arch: "amd64"})
	_ = mgr.UpdateInventory("probe-2", &protocol.InventoryPayload{CPUs: 2, MemTotal: 4 * 1024 * 1024 * 1024, OS: "linux", Arch: "amd64"})

	cmdJSON, _ := json.Marshal(FleetCommandRequest{
		Command: "uptime",
		Target:  "all",
		Reason:  "fleet uptime",
	})

	provider := &fleetMockProvider{responses: []string{string(cmdJSON), "Both probes are healthy."}}

	dispatched := map[string]int{}
	dispatch := func(probeID string, cmd *protocol.CommandPayload) (*protocol.CommandResultPayload, error) {
		dispatched[probeID]++
		return &protocol.CommandResultPayload{
			RequestID: cmd.RequestID,
			ExitCode:  0,
			Stdout:    "up 1 day",
			Duration:  7,
		}, nil
	}

	responder := NewFleetChatResponder(provider, mgr, dispatch, zap.NewNop())
	reply, err := responder.Respond(context.Background(), nil, "How is fleet uptime?")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "Both probes are healthy." {
		t.Fatalf("unexpected reply: %s", reply)
	}
	if dispatched["probe-1"] != 1 || dispatched["probe-2"] != 1 {
		t.Fatalf("expected command dispatched to both probes once, got %#v", dispatched)
	}

	if len(provider.requests) == 0 {
		t.Fatal("expected provider to receive at least one request")
	}
	systemPrompt := provider.requests[0].Messages[0].Content
	if !strings.Contains(systemPrompt, "probe-1") || !strings.Contains(systemPrompt, "probe-2") {
		t.Fatalf("fleet context missing probe summaries: %s", systemPrompt)
	}
}
