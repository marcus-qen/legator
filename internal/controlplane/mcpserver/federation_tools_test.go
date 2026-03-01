package mcpserver

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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

func normalizeFederatedInventoryForCompare(inventory *fleet.FederatedInventory) {
	if inventory == nil {
		return
	}
	for i := range inventory.Sources {
		inventory.Sources[i].CollectedAt = time.Time{}
	}
}

func normalizeFederatedSummaryForCompare(summary *fleet.FederatedInventorySummary) {
	if summary == nil {
		return
	}
	for i := range summary.Sources {
		summary.Sources[i].CollectedAt = time.Time{}
	}
}
