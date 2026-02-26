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

func TestScanAddsKubernetesMetadataWhenRunningInCluster(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	t.Setenv("NODE_NAME", "worker-1")
	t.Setenv("POD_NAME", "legator-probe-abc")
	t.Setenv("POD_NAMESPACE", "legator-system")

	inv, err := Scan("probe-k8s")
	if err != nil {
		t.Fatalf("scan returned error: %v", err)
	}

	if inv.Metadata["k8s_node"] != "worker-1" {
		t.Fatalf("expected k8s_node metadata, got %q", inv.Metadata["k8s_node"])
	}
	if inv.Metadata["k8s_pod"] != "legator-probe-abc" {
		t.Fatalf("expected k8s_pod metadata, got %q", inv.Metadata["k8s_pod"])
	}
	if inv.Metadata["k8s_namespace"] != "legator-system" {
		t.Fatalf("expected k8s_namespace metadata, got %q", inv.Metadata["k8s_namespace"])
	}
	if inv.Metadata["k8s_cluster"] != "true" {
		t.Fatalf("expected k8s_cluster metadata true, got %q", inv.Metadata["k8s_cluster"])
	}
}
