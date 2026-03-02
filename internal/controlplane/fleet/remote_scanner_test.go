package fleet

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestRemoteScannerScanProbeSuccessUpdatesLifecycleAndInventory(t *testing.T) {
	mgr := NewManager(testLogger())
	ps, err := mgr.RegisterRemote(RemoteProbeRegistration{
		ID:       "rpr-scan-1",
		Hostname: "remote-1",
		Remote: RemoteProbeConfig{
			Host:     "10.0.0.11",
			Port:     22,
			Username: "root",
		},
		Credentials: RemoteProbeCredentials{Password: "secret"},
	})
	if err != nil {
		t.Fatalf("register remote probe: %v", err)
	}

	runner := &fakeRemoteRunner{results: map[string]*RemoteRunResult{
		"hostname": {Stdout: "remote-1\n", ExitCode: 0},
		"uname -s": {Stdout: "Linux\n", ExitCode: 0},
		"uname -m": {Stdout: "x86_64\n", ExitCode: 0},
		"uname -r": {Stdout: "6.8.0\n", ExitCode: 0},
		"getconf _NPROCESSORS_ONLN 2>/dev/null || nproc 2>/dev/null || echo 1": {Stdout: "2\n", ExitCode: 0},
		"awk '/MemTotal/ {print $2*1024}' /proc/meminfo 2>/dev/null || echo 0": {Stdout: "2147483648\n", ExitCode: 0},
		"df -B1 --total 2>/dev/null | awk '/total/ {print $2}' | tail -n1":   {Stdout: "4294967296\n", ExitCode: 0},
	}}
	executor := &RemoteExecutor{
		runner:           runner,
		defaultTimeout:   30 * time.Second,
		inventoryTimeout: 45 * time.Second,
		maxOutputBytes:   128 * 1024,
		now:              func() time.Time { return time.Now().UTC() },
	}

	scanner := NewRemoteScanner(mgr, executor, zap.NewNop(), time.Minute)
	scanner.ScanProbe(context.Background(), ps.ID)

	updated, ok := mgr.Get(ps.ID)
	if !ok {
		t.Fatalf("probe %s missing after scan", ps.ID)
	}
	if updated.Status != "online" {
		t.Fatalf("expected status online, got %s", updated.Status)
	}
	if updated.Inventory == nil {
		t.Fatal("expected inventory to be populated")
	}
	if updated.Inventory.Hostname != "remote-1" {
		t.Fatalf("unexpected inventory hostname: %+v", updated.Inventory)
	}
}

func TestRemoteScannerScanProbeFailureMarksOffline(t *testing.T) {
	mgr := NewManager(testLogger())
	ps, err := mgr.RegisterRemote(RemoteProbeRegistration{
		ID:       "rpr-scan-fail",
		Hostname: "remote-fail",
		Remote: RemoteProbeConfig{
			Host:     "10.0.0.12",
			Port:     22,
			Username: "root",
		},
		Credentials: RemoteProbeCredentials{Password: "secret"},
	})
	if err != nil {
		t.Fatalf("register remote probe: %v", err)
	}

	runner := &fakeRemoteRunner{errs: map[string]error{
		"hostname": fmt.Errorf("ssh handshake failed"),
	}}
	executor := &RemoteExecutor{
		runner:           runner,
		defaultTimeout:   30 * time.Second,
		inventoryTimeout: 45 * time.Second,
		maxOutputBytes:   128 * 1024,
		now:              func() time.Time { return time.Now().UTC() },
	}

	scanner := NewRemoteScanner(mgr, executor, zap.NewNop(), time.Minute)
	scanner.ScanProbe(context.Background(), ps.ID)

	updated, ok := mgr.Get(ps.ID)
	if !ok {
		t.Fatalf("probe %s missing after scan", ps.ID)
	}
	if updated.Status != "offline" {
		t.Fatalf("expected status offline, got %s", updated.Status)
	}
}
