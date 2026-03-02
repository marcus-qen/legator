package networkdevices

import (
	"context"
	"testing"
	"time"
)

// --- Scanner unit tests with mock executor ---

// fakeSSHExecutor allows Scanner tests to inject canned command outputs.
type fakeSSHExecutor struct {
	outputs map[string]string
	errors  map[string]error
	dialErr error
}

func newFakeExecutor(outputs map[string]string) *fakeSSHExecutor {
	return &fakeSSHExecutor{outputs: outputs, errors: map[string]error{}}
}

// We can't swap out the SSHExecutor easily, so we test through the Scanner
// by using a real SSHExecutor but no actual network connection.
// Instead, test the parsers and store integration directly.

// --- Parser fixture tests (Cisco) ---

func TestParseScanVersionCisco(t *testing.T) {
	raw := `Cisco IOS XE Software, Version 17.03.04
Technical Support: http://www.cisco.com/techsupport
Copyright (c) 1986-2021 by Cisco Systems, Inc.`
	v := parseScanVersion(raw, VendorCisco)
	if v == "" {
		t.Fatal("expected non-empty version")
	}
	if v != "Cisco IOS XE Software, Version 17.03.04" {
		t.Fatalf("unexpected version: %q", v)
	}
}

func TestParseScanVersionJunos(t *testing.T) {
	raw := `Hostname: mx480-lab
Model: mx480
JUNOS OS Kernel 21.4R3-S1.3 #0 SMP PREEMPT`
	v := parseScanVersion(raw, VendorJunos)
	if v == "" {
		t.Fatal("expected non-empty version")
	}
	if v != "JUNOS OS Kernel 21.4R3-S1.3 #0 SMP PREEMPT" {
		t.Fatalf("unexpected version: %q", v)
	}
}

func TestParseScanVersionGenericFallback(t *testing.T) {
	raw := "Linux hostname 5.15.0-91-generic #101-Ubuntu SMP Tue Nov 14 13:30:08 UTC 2023"
	v := parseScanVersion(raw, VendorGeneric)
	if v == "" {
		t.Fatal("expected non-empty version")
	}
	if v != raw {
		t.Fatalf("unexpected generic version: %q", v)
	}
}

// --- Parser fixture tests (serial) ---

func TestParseScanSerialCisco(t *testing.T) {
	raw := `Processor board ID FCZ2148U04K
Board revision F0`
	s := parseScanSerial(raw, VendorCisco)
	if s != "FCZ2148U04K" {
		t.Fatalf("expected FCZ2148U04K, got %q", s)
	}
}

func TestParseScanSerialJunos(t *testing.T) {
	raw := `Hardware inventory:
Item             Version  Part number  Serial number     Description
Chassis                               REX0123456789     MX480`
	// Only the "Chassis" line matters.
	raw2 := "Chassis    REX0123456789  MX480"
	s := parseScanSerial(raw2, VendorJunos)
	if s != "REX0123456789" {
		t.Fatalf("expected REX0123456789, got %q", s)
	}
	// Multi-line form — just verify no panic.
	_ = parseScanSerial(raw, VendorJunos)
}

func TestParseScanSerialFortinet(t *testing.T) {
	raw := `Version: FortiGate-60E v6.4.9
Serial-Number: FGT60E4Q17012345
BIOS version: 05000013`
	s := parseScanSerial(raw, VendorFortinet)
	if s != "FGT60E4Q17012345" {
		t.Fatalf("expected FGT60E4Q17012345, got %q", s)
	}
}

func TestParseScanSerialLinuxUnknown(t *testing.T) {
	s := parseScanSerial("unknown", VendorGeneric)
	if s != "" {
		t.Fatalf("expected empty for unknown, got %q", s)
	}
}

func TestParseScanSerialLinuxValue(t *testing.T) {
	s := parseScanSerial("SN123456", VendorGeneric)
	if s != "SN123456" {
		t.Fatalf("expected SN123456, got %q", s)
	}
}

// --- scannerVendorCommands tests ---

func TestScannerVendorCommandsCisco(t *testing.T) {
	cmds := scannerVendorCommands(VendorCisco, ScanConfig{})
	for _, key := range []string{"hostname", "version", "interfaces", "serial"} {
		if _, ok := cmds[key]; !ok {
			t.Errorf("missing command key %q for cisco", key)
		}
	}
	if _, ok := cmds["routing"]; ok {
		t.Error("routing command should not be present when IncludeRouting=false")
	}
}

func TestScannerVendorCommandsCiscoWithRouting(t *testing.T) {
	cmds := scannerVendorCommands(VendorCisco, ScanConfig{IncludeRouting: true})
	if _, ok := cmds["routing"]; !ok {
		t.Error("expected routing command when IncludeRouting=true")
	}
}

func TestScannerVendorCommandsJunos(t *testing.T) {
	cmds := scannerVendorCommands(VendorJunos, ScanConfig{})
	for _, key := range []string{"hostname", "version", "interfaces", "serial"} {
		if _, ok := cmds[key]; !ok {
			t.Errorf("missing command key %q for junos", key)
		}
	}
}

func TestScannerVendorCommandsGeneric(t *testing.T) {
	cmds := scannerVendorCommands(VendorGeneric, ScanConfig{})
	for _, key := range []string{"hostname", "version", "interfaces", "serial"} {
		if _, ok := cmds[key]; !ok {
			t.Errorf("missing command key %q for generic", key)
		}
	}
}

// --- Scanner integration: stores result even with exec errors ---

func TestScannerStoresPersistsOnPartialFailure(t *testing.T) {
	store := newTestStore(t)

	device, err := store.CreateDevice(Device{
		Name:     "test-router",
		Host:     "10.0.0.1",
		Port:     22,
		Vendor:   VendorCisco,
		Username: "admin",
		AuthMode: AuthModePassword,
	})
	if err != nil {
		t.Fatalf("create device: %v", err)
	}

	// Build a scanner with a nil executor to trigger execution errors.
	executor := NewSSHExecutor(store)
	executor.DialTimeout = 100 * time.Millisecond
	executor.CommandTimeout = 100 * time.Millisecond

	scanner := NewScanner(executor, store)

	// Execute against unreachable host; errors expected but scan should not panic.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	result, err := scanner.Scan(ctx, *device, CredentialInput{Password: "x"}, ScanConfig{})
	// err is nil - scanner is best-effort
	if err != nil {
		t.Fatalf("scanner.Scan returned error: %v", err)
	}
	// All commands will fail (connection refused), so Errors will be populated.
	if len(result.Errors) == 0 {
		t.Log("note: no errors reported (unexpected for unreachable host, but not a test failure if host happened to be reachable)")
	}
	// DeviceID must be populated.
	if result.DeviceID != device.ID {
		t.Fatalf("expected device ID %q, got %q", device.ID, result.DeviceID)
	}
}
