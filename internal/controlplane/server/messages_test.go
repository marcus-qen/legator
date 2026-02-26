package server

import (
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/protocol"
)

func TestHandleProbeMessage_HeartbeatAutoRegistersProbe(t *testing.T) {
	srv := newTestServer(t)

	hb := protocol.HeartbeatPayload{
		ProbeID:   "probe-heartbeat",
		Uptime:    42,
		Load:      [3]float64{0.25, 0.1, 0.05},
		MemUsed:   256,
		MemTotal:  1024,
		DiskUsed:  1024,
		DiskTotal: 4096,
	}

	srv.handleProbeMessage("probe-heartbeat", protocol.Envelope{
		Type:    protocol.MsgHeartbeat,
		Payload: hb,
	})

	ps, ok := srv.fleetMgr.Get("probe-heartbeat")
	if !ok {
		t.Fatal("expected probe to be auto-registered from heartbeat")
	}
	if ps.Health == nil {
		t.Fatal("expected health score to be populated")
	}
	if ps.Status == "offline" {
		t.Fatalf("expected non-offline status after heartbeat, got %q", ps.Status)
	}

	auditEvents := srv.queryAudit(audit.Filter{ProbeID: "probe-heartbeat", Type: audit.EventProbeRegistered, Limit: 5})
	if len(auditEvents) == 0 {
		t.Fatal("expected audit event for auto-registration")
	}
}

func TestHandleProbeMessage_InventoryUpdatesState(t *testing.T) {
	srv := newTestServer(t)
	srv.fleetMgr.Register("probe-inv", "host", "linux", "amd64")

	inv := protocol.InventoryPayload{
		ProbeID:   "probe-inv",
		Hostname:  "host",
		OS:        "linux",
		Arch:      "amd64",
		CPUs:      8,
		MemTotal:  32 * 1024,
		DiskTotal: 128 * 1024,
	}

	srv.handleProbeMessage("probe-inv", protocol.Envelope{
		Type:    protocol.MsgInventory,
		Payload: inv,
	})

	ps, ok := srv.fleetMgr.Get("probe-inv")
	if !ok {
		t.Fatal("probe missing")
	}
	if ps.Inventory == nil || ps.Inventory.CPUs != 8 {
		t.Fatalf("expected inventory CPUs=8, got %+v", ps.Inventory)
	}

	auditEvents := srv.queryAudit(audit.Filter{ProbeID: "probe-inv", Type: audit.EventInventoryUpdate, Limit: 1})
	if len(auditEvents) != 1 {
		t.Fatalf("expected inventory audit event, got %d", len(auditEvents))
	}
}

func TestHandleProbeMessage_CommandResultCompletesPendingCommand(t *testing.T) {
	srv := newTestServer(t)
	pending := srv.cmdTracker.Track("req-command-result", "probe-cmd", "ls", protocol.CapObserve)

	srv.handleProbeMessage("probe-cmd", protocol.Envelope{
		Type: protocol.MsgCommandResult,
		Payload: protocol.CommandResultPayload{
			RequestID: "req-command-result",
			ExitCode:  0,
			Stdout:    "ok",
			Duration:  15,
		},
	})

	select {
	case result := <-pending.Result:
		if result == nil {
			t.Fatal("expected command result, got nil")
		}
		if result.ExitCode != 0 || result.Stdout != "ok" {
			t.Fatalf("unexpected command result: %+v", result)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for command result completion")
	}

	auditEvents := srv.queryAudit(audit.Filter{ProbeID: "probe-cmd", Type: audit.EventCommandResult, Limit: 1})
	if len(auditEvents) != 1 {
		t.Fatalf("expected command result audit event, got %d", len(auditEvents))
	}
}

func TestHandleProbeMessage_OutputChunkFinalCompletesPendingCommand(t *testing.T) {
	srv := newTestServer(t)
	pending := srv.cmdTracker.Track("req-stream-final", "probe-stream", "tail -f", protocol.CapObserve)

	srv.handleProbeMessage("probe-stream", protocol.Envelope{
		Type: protocol.MsgOutputChunk,
		Payload: protocol.OutputChunkPayload{
			RequestID: "req-stream-final",
			Stream:    "stdout",
			Data:      "done",
			Seq:       3,
			Final:     true,
			ExitCode:  17,
		},
	})

	select {
	case result := <-pending.Result:
		if result == nil {
			t.Fatal("expected result for final output chunk")
		}
		if result.ExitCode != 17 {
			t.Fatalf("expected exit_code=17, got %+v", result)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for final chunk completion")
	}
}
