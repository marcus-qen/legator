package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	resourceFleetSummary   = "legator://fleet/summary"
	resourceFleetInventory = "legator://fleet/inventory"
)

func (s *MCPServer) registerResources() {
	s.server.AddResource(&mcp.Resource{
		URI:         resourceFleetSummary,
		Name:        "Fleet Summary",
		Description: "Fleet-wide status counts, tags, and pending approval totals",
		MIMEType:    "application/json",
	}, s.handleFleetSummaryResource)

	s.server.AddResource(&mcp.Resource{
		URI:         resourceFleetInventory,
		Name:        "Fleet Inventory",
		Description: "Aggregated fleet inventory across all probes",
		MIMEType:    "application/json",
	}, s.handleFleetInventoryResource)
}

func (s *MCPServer) handleFleetSummaryResource(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if s.fleetStore == nil {
		return nil, fmt.Errorf("fleet store unavailable")
	}

	counts := s.fleetStore.Count()
	tags := s.fleetStore.TagCounts()
	total := 0
	for _, c := range counts {
		total += c
	}

	payload := map[string]any{
		"total_probes":      total,
		"by_status":         counts,
		"tags":              tags,
		"pending_approvals": 0,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	uri := resourceFleetSummary
	if req != nil && req.Params != nil && req.Params.URI != "" {
		uri = req.Params.URI
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(data),
		}},
	}, nil
}

func (s *MCPServer) handleFleetInventoryResource(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if s.fleetStore == nil {
		return nil, fmt.Errorf("fleet store unavailable")
	}

	inventory := s.fleetStore.Inventory(fleet.InventoryFilter{})
	data, err := json.Marshal(inventory)
	if err != nil {
		return nil, err
	}

	uri := resourceFleetInventory
	if req != nil && req.Params != nil && req.Params.URI != "" {
		uri = req.Params.URI
	}

	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(data),
		}},
	}, nil
}
