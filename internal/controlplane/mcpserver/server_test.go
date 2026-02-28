package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/controlplane/events"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	cpws "github.com/marcus-qen/legator/internal/controlplane/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

func TestToolsRegistered(t *testing.T) {
	srv, _, _, _ := newTestMCPServer(t)
	session := connectClient(t, srv)

	result, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)

	expected := []string{
		"legator_decide_approval",
		"legator_fleet_query",
		"legator_get_inventory",
		"legator_get_job_run",
		"legator_list_job_runs",
		"legator_list_jobs",
		"legator_list_probes",
		"legator_poll_job_active",
		"legator_probe_health",
		"legator_probe_info",
		"legator_run_command",
		"legator_search_audit",
		"legator_stream_job_events",
		"legator_stream_job_run_output",
	}

	if len(names) != len(expected) {
		t.Fatalf("expected %d tools, got %d: %v", len(expected), len(names), names)
	}
	for i := range expected {
		if names[i] != expected[i] {
			t.Fatalf("unexpected tool list: got %v want %v", names, expected)
		}
	}
}

func TestListProbesTool(t *testing.T) {
	srv, fleetStore, _, _ := newTestMCPServer(t)
	fleetStore.Register("probe-a", "host-a", "linux", "amd64")
	fleetStore.Register("probe-b", "host-b", "linux", "arm64")
	if err := fleetStore.SetTags("probe-a", []string{"prod", "db"}); err != nil {
		t.Fatalf("set tags probe-a: %v", err)
	}
	if err := fleetStore.SetTags("probe-b", []string{"dev"}); err != nil {
		t.Fatalf("set tags probe-b: %v", err)
	}
	if ps, ok := fleetStore.Get("probe-b"); ok {
		ps.LastSeen = time.Now().UTC().Add(-2 * time.Hour)
	}
	fleetStore.MarkOffline(30 * time.Minute)

	session := connectClient(t, srv)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "legator_list_probes",
		Arguments: map[string]any{
			"status": "offline",
		},
	})
	if err != nil {
		t.Fatalf("call legator_list_probes: %v", err)
	}

	var probes []probeSummary
	decodeToolJSON(t, result, &probes)
	if len(probes) != 1 {
		t.Fatalf("expected 1 offline probe, got %d (%+v)", len(probes), probes)
	}
	if probes[0].ID != "probe-b" {
		t.Fatalf("expected probe-b, got %s", probes[0].ID)
	}
}

func TestProbeInfoTool(t *testing.T) {
	srv, fleetStore, _, _ := newTestMCPServer(t)
	fleetStore.Register("probe-info", "host-info", "linux", "amd64")

	session := connectClient(t, srv)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "legator_probe_info",
		Arguments: map[string]any{
			"probe_id": "probe-info",
		},
	})
	if err != nil {
		t.Fatalf("call legator_probe_info: %v", err)
	}

	var probe fleet.ProbeState
	decodeToolJSON(t, result, &probe)
	if probe.ID != "probe-info" || probe.Hostname != "host-info" {
		t.Fatalf("unexpected probe info: %+v", probe)
	}
}

func TestSearchAuditTool(t *testing.T) {
	srv, _, auditStore, _ := newTestMCPServer(t)
	auditStore.Record(audit.Event{Type: audit.EventCommandSent, ProbeID: "probe-a", Actor: "tester", Summary: "sent command"})
	auditStore.Record(audit.Event{Type: audit.EventProbeRegistered, ProbeID: "probe-b", Actor: "tester", Summary: "registered"})

	session := connectClient(t, srv)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "legator_search_audit",
		Arguments: map[string]any{
			"probe_id": "probe-a",
			"type":     string(audit.EventCommandSent),
			"limit":    10,
		},
	})
	if err != nil {
		t.Fatalf("call legator_search_audit: %v", err)
	}

	var events []audit.Event
	decodeToolJSON(t, result, &events)
	if len(events) == 0 {
		t.Fatal("expected at least one audit event")
	}
	if events[0].ProbeID != "probe-a" {
		t.Fatalf("expected probe-a event, got %+v", events[0])
	}
	if events[0].Type != audit.EventCommandSent {
		t.Fatalf("expected type %s, got %s", audit.EventCommandSent, events[0].Type)
	}
}

func newTestMCPServer(t *testing.T) (*MCPServer, *fleet.Store, *audit.Store, *jobs.Store) {
	t.Helper()
	dir := t.TempDir()

	fleetStore, err := fleet.NewStore(filepath.Join(dir, "fleet.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("new fleet store: %v", err)
	}
	auditStore, err := audit.NewStore(filepath.Join(dir, "audit.db"), 1000)
	if err != nil {
		_ = fleetStore.Close()
		t.Fatalf("new audit store: %v", err)
	}
	jobsStore, err := jobs.NewStore(filepath.Join(dir, "jobs.db"))
	if err != nil {
		_ = fleetStore.Close()
		_ = auditStore.Close()
		t.Fatalf("new jobs store: %v", err)
	}

	eventBus := events.NewBus(64)
	hub := cpws.NewHub(zap.NewNop(), nil)
	tracker := cmdtracker.New(time.Minute)
	srv := New(fleetStore, auditStore, jobsStore, eventBus, hub, tracker, zap.NewNop(), nil)

	t.Cleanup(func() {
		_ = fleetStore.Close()
		_ = auditStore.Close()
		_ = jobsStore.Close()
	})

	return srv, fleetStore, auditStore, jobsStore
}

func connectClient(t *testing.T, srv *MCPServer) *mcp.ClientSession {
	t.Helper()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.server.Run(runCtx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("connect client: %v", err)
	}

	t.Cleanup(func() {
		_ = session.Close()
		cancel()
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Logf("mcp server run exited with: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Log("timed out waiting for mcp server shutdown")
		}
	})

	return session
}

func decodeToolJSON(t *testing.T, result *mcp.CallToolResult, out any) {
	t.Helper()
	if result == nil || len(result.Content) == 0 {
		t.Fatalf("empty tool result: %#v", result)
	}

	var text string
	switch content := result.Content[0].(type) {
	case *mcp.TextContent:
		text = content.Text
	default:
		t.Fatalf("unexpected content type %T", result.Content[0])
	}

	if err := json.Unmarshal([]byte(text), out); err != nil {
		t.Fatalf("decode tool json: %v (text=%q)", err, text)
	}
}
