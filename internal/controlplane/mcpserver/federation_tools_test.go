package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpStubFederationAdapter struct {
	source fleet.FederationSourceDescriptor
	result fleet.FederationSourceResult
	err    error
}

func (a *mcpStubFederationAdapter) Source() fleet.FederationSourceDescriptor {
	return a.source
}

func (a *mcpStubFederationAdapter) Inventory(_ context.Context, _ fleet.InventoryFilter) (fleet.FederationSourceResult, error) {
	if a.err != nil {
		return fleet.FederationSourceResult{}, a.err
	}
	return a.result, nil
}

func TestFederationToolsAndResourcesParityWithFilters(t *testing.T) {
	srv, fleetStore, _, _ := newTestMCPServer(t)

	fleetStore.Register("probe-web", "web-01", "linux", "amd64")
	fleetStore.Register("probe-db", "db-01", "linux", "amd64")
	_ = fleetStore.SetTags("probe-web", []string{"prod", "frontend"})
	_ = fleetStore.SetTags("probe-db", []string{"prod", "database"})
	_ = fleetStore.UpdateInventory("probe-web", &protocol.InventoryPayload{OS: "linux", CPUs: 4, MemTotal: 8 * 1024 * 1024 * 1024})
	_ = fleetStore.UpdateInventory("probe-db", &protocol.InventoryPayload{OS: "linux", CPUs: 2, MemTotal: 4 * 1024 * 1024 * 1024})
	if ps, ok := fleetStore.Get("probe-db"); ok {
		ps.Status = "offline"
	}

	session := connectClient(t, srv)
	args := map[string]any{
		"tag":     "prod",
		"status":  "online",
		"source":  "local",
		"cluster": "primary",
		"site":    "local",
		"search":  "web",
	}
	filter := fleet.FederationFilter{
		Tag:     "prod",
		Status:  "online",
		Source:  "local",
		Cluster: "primary",
		Site:    "local",
		Search:  "web",
	}

	invResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "legator_federation_inventory", Arguments: args})
	if err != nil {
		t.Fatalf("call legator_federation_inventory: %v", err)
	}
	var invPayload fleet.FederatedInventory
	decodeToolJSON(t, invResult, &invPayload)

	expectedInv := srv.federationStore.Inventory(context.Background(), filter)
	normalizeFederatedInventoryForCompare(&invPayload)
	normalizeFederatedInventoryForCompare(&expectedInv)
	if !reflect.DeepEqual(invPayload, expectedInv) {
		t.Fatalf("inventory parity mismatch:\nmcp=%+v\nexpected=%+v", invPayload, expectedInv)
	}
	if invPayload.Consistency.Freshness != fleet.FederationFreshnessFresh || invPayload.Consistency.Completeness != fleet.FederationCompletenessComplete {
		t.Fatalf("expected MCP inventory consistency markers, got %+v", invPayload.Consistency)
	}

	summaryResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "legator_federation_summary", Arguments: args})
	if err != nil {
		t.Fatalf("call legator_federation_summary: %v", err)
	}
	var summaryPayload fleet.FederatedInventorySummary
	decodeToolJSON(t, summaryResult, &summaryPayload)

	expectedSummary := srv.federationStore.Summary(context.Background(), filter)
	normalizeFederatedSummaryForCompare(&summaryPayload)
	normalizeFederatedSummaryForCompare(&expectedSummary)
	if !reflect.DeepEqual(summaryPayload, expectedSummary) {
		t.Fatalf("summary parity mismatch:\nmcp=%+v\nexpected=%+v", summaryPayload, expectedSummary)
	}

	resourceInv, err := srv.handleFederationInventoryResource(context.Background(), &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: "legator://federation/inventory?tag=prod&status=online&source=local&cluster=primary&site=local&search=web"}})
	if err != nil {
		t.Fatalf("read federation inventory resource: %v", err)
	}
	var resourceInvPayload fleet.FederatedInventory
	if err := json.Unmarshal([]byte(resourceInv.Contents[0].Text), &resourceInvPayload); err != nil {
		t.Fatalf("decode federation inventory resource payload: %v", err)
	}
	normalizeFederatedInventoryForCompare(&resourceInvPayload)
	if !reflect.DeepEqual(resourceInvPayload, expectedInv) {
		t.Fatalf("resource inventory parity mismatch:\nresource=%+v\nexpected=%+v", resourceInvPayload, expectedInv)
	}

	resourceSummary, err := srv.handleFederationSummaryResource(context.Background(), &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: "legator://federation/summary?tag=prod&status=online&source=local&cluster=primary&site=local&search=web"}})
	if err != nil {
		t.Fatalf("read federation summary resource: %v", err)
	}
	var resourceSummaryPayload fleet.FederatedInventorySummary
	if err := json.Unmarshal([]byte(resourceSummary.Contents[0].Text), &resourceSummaryPayload); err != nil {
		t.Fatalf("decode federation summary resource payload: %v", err)
	}
	normalizeFederatedSummaryForCompare(&resourceSummaryPayload)
	if !reflect.DeepEqual(resourceSummaryPayload, expectedSummary) {
		t.Fatalf("resource summary parity mismatch:\nresource=%+v\nexpected=%+v", resourceSummaryPayload, expectedSummary)
	}
}

func TestFederationResourcesRegistered(t *testing.T) {
	srv, _, _, _ := newTestMCPServer(t)
	session := connectClient(t, srv)

	resourcesResult, err := session.ListResources(context.Background(), &mcp.ListResourcesParams{})
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}

	resourceURIs := make([]string, 0, len(resourcesResult.Resources))
	for _, resource := range resourcesResult.Resources {
		resourceURIs = append(resourceURIs, resource.URI)
	}
	sort.Strings(resourceURIs)

	for _, expected := range []string{resourceFederationInventory, resourceFederationSummary} {
		if !containsString(resourceURIs, expected) {
			t.Fatalf("expected resource %s in %v", expected, resourceURIs)
		}
	}
}

func TestFederationMCPPermissionCoverage(t *testing.T) {
	deniedErr := errors.New("insufficient permissions (required: fleet:read)")
	requestedPerms := make([]auth.Permission, 0, 4)
	srv, _, _, _ := newTestMCPServerWithOptions(t,
		WithPermissionChecker(func(_ context.Context, perm auth.Permission) error {
			requestedPerms = append(requestedPerms, perm)
			return deniedErr
		}),
	)

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{name: "tool inventory", call: func() error {
			_, _, err := srv.handleFederationInventory(context.Background(), nil, federationQueryInput{})
			return err
		}},
		{name: "tool summary", call: func() error {
			_, _, err := srv.handleFederationSummary(context.Background(), nil, federationQueryInput{})
			return err
		}},
		{name: "resource inventory", call: func() error {
			_, err := srv.handleFederationInventoryResource(context.Background(), nil)
			return err
		}},
		{name: "resource summary", call: func() error {
			_, err := srv.handleFederationSummaryResource(context.Background(), nil)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, deniedErr) {
				t.Fatalf("expected denied error, got %v", err)
			}
		})
	}

	if len(requestedPerms) != 4 {
		t.Fatalf("expected 4 permission checks, got %d (%v)", len(requestedPerms), requestedPerms)
	}
	for _, perm := range requestedPerms {
		if perm != auth.PermFleetRead {
			t.Fatalf("expected fleet:read permission check, got %s", perm)
		}
	}
}

func TestFederationMCPScopedAuthorizationAndAuditAttribution(t *testing.T) {
	srv, _, auditStore, _ := newTestMCPServer(t)
	srv.federationStore.RegisterSource(&mcpStubFederationAdapter{
		source: fleet.FederationSourceDescriptor{ID: "tenant-a", Name: "Tenant A", Kind: "cluster", Cluster: "primary", Site: "dc-1", TenantID: "tenant-a", OrgID: "org-a", ScopeID: "scope-a"},
		result: fleet.FederationSourceResult{Inventory: fleet.FleetInventory{Probes: []fleet.ProbeInventorySummary{{ID: "probe-a", Hostname: "a-1", Status: "online", OS: "linux"}}}},
	})
	srv.federationStore.RegisterSource(&mcpStubFederationAdapter{
		source: fleet.FederationSourceDescriptor{ID: "tenant-b", Name: "Tenant B", Kind: "cluster", Cluster: "primary", Site: "dc-2", TenantID: "tenant-b", OrgID: "org-b", ScopeID: "scope-b"},
		result: fleet.FederationSourceResult{Inventory: fleet.FleetInventory{Probes: []fleet.ProbeInventorySummary{{ID: "probe-b", Hostname: "b-1", Status: "online", OS: "linux"}}}},
	})

	ctx := auth.ContextWithAPIKey(context.Background(), &auth.APIKey{
		Name: "tenant-a-reader",
		Permissions: []auth.Permission{
			auth.PermFleetRead,
			auth.Permission("tenant:tenant-a"),
			auth.Permission("org:org-a"),
			auth.Permission("scope:scope-a"),
		},
	})

	result, _, err := srv.handleFederationInventory(ctx, nil, federationQueryInput{})
	if err != nil {
		t.Fatalf("expected scoped inventory read to succeed, got %v", err)
	}
	var payload fleet.FederatedInventory
	decodeToolJSON(t, result, &payload)
	if len(payload.Probes) != 1 || payload.Probes[0].Probe.ID != "probe-a" {
		t.Fatalf("expected tenant-a segmented result, got %+v", payload.Probes)
	}
	if payload.Probes[0].Source.ScopeID != "scope-a" {
		t.Fatalf("expected scope attribution in result, got %+v", payload.Probes[0].Source)
	}

	_, _, err = srv.handleFederationSummary(ctx, nil, federationQueryInput{TenantID: "tenant-b", ScopeID: "scope-b"})
	if err == nil {
		t.Fatal("expected forbidden scoped access error")
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("expected scoped authz denial error, got %v", err)
	}

	denials := auditStore.Query(audit.Filter{Type: audit.EventAuthorizationDenied, Limit: 10})
	if len(denials) == 0 {
		t.Fatal("expected authorization denied audit event")
	}
	foundDeniedContext := false
	for _, evt := range denials {
		detail, ok := evt.Detail.(map[string]any)
		if !ok {
			continue
		}
		if detail["surface"] != "tool:legator_federation_summary" {
			continue
		}
		if detail["requested_tenant_id"] == "tenant-b" && detail["requested_scope_id"] == "scope-b" {
			foundDeniedContext = true
			break
		}
	}
	if !foundDeniedContext {
		t.Fatalf("expected denied audit attribution with tenant/scope context, events=%+v", denials)
	}

	reads := auditStore.Query(audit.Filter{Type: audit.EventFederationRead, Limit: 10})
	if len(reads) == 0 {
		t.Fatal("expected federation read audit events")
	}
}

func TestFederationMCPFailoverConsistencyIndicators(t *testing.T) {
	srv, _, _, _ := newTestMCPServer(t)
	now := time.Date(2026, time.March, 1, 13, 0, 0, 0, time.UTC)
	adapter := &mcpStubFederationAdapter{
		source: fleet.FederationSourceDescriptor{ID: "remote-failover", Name: "Remote Failover", Kind: "cluster", Cluster: "eu-west", Site: "dc-2"},
		result: fleet.FederationSourceResult{
			CollectedAt: now,
			Inventory: fleet.FleetInventory{Probes: []fleet.ProbeInventorySummary{{
				ID:       "probe-a",
				Hostname: "a-1",
				Status:   "online",
				OS:       "linux",
			}}},
		},
	}
	srv.federationStore.RegisterSource(adapter)
	ctx := context.Background()
	if _, _, err := srv.handleFederationInventory(ctx, nil, federationQueryInput{Source: "remote-failover"}); err != nil {
		t.Fatalf("prime cached snapshot: %v", err)
	}

	adapter.err = errors.New("source offline")
	result, _, err := srv.handleFederationSummary(ctx, nil, federationQueryInput{Source: "remote-failover"})
	if err != nil {
		t.Fatalf("summary during failover: %v", err)
	}
	var payload fleet.FederatedInventorySummary
	decodeToolJSON(t, result, &payload)

	if payload.Health.Overall != fleet.FederationSourceDegraded {
		t.Fatalf("expected degraded health during failover, got %+v", payload.Health)
	}
	if payload.Consistency.FailoverSources != 1 || !payload.Consistency.FailoverActive {
		t.Fatalf("expected failover consistency rollup, got %+v", payload.Consistency)
	}
	if len(payload.Sources) != 1 {
		t.Fatalf("expected one source in payload, got %+v", payload.Sources)
	}
	source := payload.Sources[0]
	if source.Consistency.FailoverMode != fleet.FederationFailoverCachedSnapshot || source.Consistency.Completeness != fleet.FederationCompletenessPartial {
		t.Fatalf("expected source failover semantics, got %+v", source.Consistency)
	}
	if source.Error == "" {
		t.Fatalf("expected source outage detail in failover payload, got %+v", source)
	}
}

func normalizeFederatedInventoryForCompare(inventory *fleet.FederatedInventory) {
	if inventory == nil {
		return
	}
	for i := range inventory.Sources {
		inventory.Sources[i].CollectedAt = time.Time{}
		inventory.Sources[i].Consistency.SnapshotAgeSeconds = 0
	}
	for i := range inventory.Health.Sources {
		inventory.Health.Sources[i].Consistency.SnapshotAgeSeconds = 0
	}
}

func normalizeFederatedSummaryForCompare(summary *fleet.FederatedInventorySummary) {
	if summary == nil {
		return
	}
	for i := range summary.Sources {
		summary.Sources[i].CollectedAt = time.Time{}
		summary.Sources[i].Consistency.SnapshotAgeSeconds = 0
	}
	for i := range summary.Health.Sources {
		summary.Health.Sources[i].Consistency.SnapshotAgeSeconds = 0
	}
}
