package inventory

import "testing"

func TestScanReturnsNonEmptyHostname(t *testing.T) {
	inv, err := Scan("probe-1")
	if err != nil {
		t.Fatalf("scan returned error: %v", err)
	}

	if inv.Hostname == "" {
		t.Fatal("hostname should not be empty")
	}
}

func TestScanReturnsPositiveCPUs(t *testing.T) {
	inv, err := Scan("probe-2")
	if err != nil {
		t.Fatalf("scan returned error: %v", err)
	}

	if inv.CPUs <= 0 {
		t.Fatalf("cpu count should be > 0, got %d", inv.CPUs)
	}
}

func TestScanPreservesProbeID(t *testing.T) {
	probeID := "probe-3"
	inv, err := Scan(probeID)
	if err != nil {
		t.Fatalf("scan returned error: %v", err)
	}

	if inv.ProbeID != probeID {
		t.Fatalf("expected probe ID %q, got %q", probeID, inv.ProbeID)
	}
}
