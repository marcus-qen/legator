package fleet

import (
	"context"
	"fmt"
	"testing"
)

type stubFederationAdapter struct {
	source  FederationSourceDescriptor
	result  FederationSourceResult
	err     error
	filters []InventoryFilter
}

func (s *stubFederationAdapter) Source() FederationSourceDescriptor {
	return s.source
}

func (s *stubFederationAdapter) Inventory(_ context.Context, filter InventoryFilter) (FederationSourceResult, error) {
	s.filters = append(s.filters, filter)
	if s.err != nil {
		return FederationSourceResult{}, s.err
	}
	return s.result, nil
}

func TestFederationStoreInventory_AggregatesAndRollsHealth(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)

	healthy := &stubFederationAdapter{
		source: FederationSourceDescriptor{ID: "cluster-a", Name: "Cluster A", Kind: "k8s", Cluster: "eu-west-1", Site: "dc-1"},
		result: FederationSourceResult{
			Inventory: FleetInventory{
				Probes: []ProbeInventorySummary{{
					ID:       "probe-a",
					Hostname: "a-1",
					Status:   "online",
					OS:       "linux",
					CPUs:     2,
					RAMBytes: 4 * gib,
					Tags:     []string{"prod"},
				}},
				Aggregates: FleetAggregates{
					TotalProbes:     1,
					Online:          1,
					TotalCPUs:       2,
					TotalRAMBytes:   4 * gib,
					ProbesByOS:      map[string]int{"linux": 1},
					TagDistribution: map[string]int{"prod": 1},
				},
			},
		},
	}

	degraded := &stubFederationAdapter{
		source: FederationSourceDescriptor{ID: "cluster-b", Name: "Cluster B", Kind: "k8s", Cluster: "eu-central-1", Site: "dc-2"},
		result: FederationSourceResult{
			Inventory: FleetInventory{
				Probes: []ProbeInventorySummary{{
					ID:       "probe-b",
					Hostname: "b-1",
					Status:   "offline",
					OS:       "windows",
					CPUs:     4,
					RAMBytes: 8 * gib,
					Tags:     []string{"dev"},
				}},
				Aggregates: FleetAggregates{
					TotalProbes:     1,
					Online:          0,
					TotalCPUs:       4,
					TotalRAMBytes:   8 * gib,
					ProbesByOS:      map[string]int{"windows": 1},
					TagDistribution: map[string]int{"dev": 1},
				},
			},
			Partial:  true,
			Warnings: []string{"partial inventory: pods unavailable"},
		},
	}

	unavailable := &stubFederationAdapter{
		source: FederationSourceDescriptor{ID: "cluster-c", Name: "Cluster C", Kind: "k8s", Cluster: "eu-north-1", Site: "dc-3"},
		err:    fmt.Errorf("upstream unreachable"),
	}

	store := NewFederationStore(healthy, degraded, unavailable)
	got := store.Inventory(context.Background(), FederationFilter{})

	if got.Aggregates.TotalSources != 3 {
		t.Fatalf("expected 3 sources, got %d", got.Aggregates.TotalSources)
	}
	if got.Aggregates.HealthySources != 1 || got.Aggregates.DegradedSources != 1 || got.Aggregates.UnavailableSources != 1 {
		t.Fatalf("unexpected source health counts: %+v", got.Aggregates)
	}
	if got.Aggregates.TotalProbes != 2 {
		t.Fatalf("expected 2 probes, got %d", got.Aggregates.TotalProbes)
	}
	if got.Aggregates.Online != 1 {
		t.Fatalf("expected 1 online probe, got %d", got.Aggregates.Online)
	}
	if got.Aggregates.TotalCPUs != 6 {
		t.Fatalf("expected 6 CPUs, got %d", got.Aggregates.TotalCPUs)
	}
	if got.Aggregates.TotalRAMBytes != 12*gib {
		t.Fatalf("expected 12GiB RAM, got %d", got.Aggregates.TotalRAMBytes)
	}
	if got.Health.Overall != FederationSourceDegraded {
		t.Fatalf("expected overall degraded health, got %q", got.Health.Overall)
	}
	if len(got.Probes) != 2 {
		t.Fatalf("expected 2 federated probes, got %d", len(got.Probes))
	}

	statuses := map[string]FederationSourceStatus{}
	for _, source := range got.Sources {
		statuses[source.Source.ID] = source.Source.Status
	}
	if statuses["cluster-a"] != FederationSourceHealthy {
		t.Fatalf("expected cluster-a healthy, got %q", statuses["cluster-a"])
	}
	if statuses["cluster-b"] != FederationSourceDegraded {
		t.Fatalf("expected cluster-b degraded, got %q", statuses["cluster-b"])
	}
	if statuses["cluster-c"] != FederationSourceUnavailable {
		t.Fatalf("expected cluster-c unavailable, got %q", statuses["cluster-c"])
	}

	seenAttribution := false
	for _, probe := range got.Probes {
		if probe.Probe.ID == "probe-a" {
			if probe.Source.ID != "cluster-a" || probe.Source.Cluster != "eu-west-1" || probe.Source.Site != "dc-1" {
				t.Fatalf("unexpected source attribution for probe-a: %+v", probe.Source)
			}
			seenAttribution = true
		}
	}
	if !seenAttribution {
		t.Fatal("expected probe-a attribution entry")
	}
}

func TestFederationStoreInventory_AppliesSourceAndInventoryFilters(t *testing.T) {
	adapter := &stubFederationAdapter{
		source: FederationSourceDescriptor{ID: "edge-a", Name: "Edge A", Kind: "k8s", Cluster: "eu-west", Site: "dc-9"},
		result: FederationSourceResult{Inventory: FleetInventory{Aggregates: FleetAggregates{ProbesByOS: map[string]int{}, TagDistribution: map[string]int{}}}},
	}

	store := NewFederationStore(adapter)

	_ = store.Inventory(context.Background(), FederationFilter{
		Tag:     "prod",
		Status:  "online",
		Source:  "edge-a",
		Cluster: "eu-west",
		Site:    "dc-9",
	})
	if len(adapter.filters) != 1 {
		t.Fatalf("expected one adapter invocation, got %d", len(adapter.filters))
	}
	if adapter.filters[0].Tag != "prod" || adapter.filters[0].Status != "online" {
		t.Fatalf("expected tag/status forwarded to source adapter, got %+v", adapter.filters[0])
	}

	_ = store.Inventory(context.Background(), FederationFilter{Source: "missing-source"})
	if len(adapter.filters) != 1 {
		t.Fatalf("expected non-matching source filter to skip adapter, got %d invocations", len(adapter.filters))
	}
}

func TestFederationStoreSummary_NoSourcesReturnsUnknown(t *testing.T) {
	store := NewFederationStore()
	summary := store.Summary(context.Background(), FederationFilter{})

	if summary.Aggregates.TotalSources != 0 {
		t.Fatalf("expected 0 sources, got %d", summary.Aggregates.TotalSources)
	}
	if summary.Health.Overall != FederationSourceUnknown {
		t.Fatalf("expected unknown overall health, got %q", summary.Health.Overall)
	}
	if len(summary.Sources) != 0 {
		t.Fatalf("expected no sources, got %d", len(summary.Sources))
	}
}
