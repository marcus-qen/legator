package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	resourceFleetSummary          = "legator://fleet/summary"
	resourceFleetInventory        = "legator://fleet/inventory"
	resourceFederationInventory   = "legator://federation/inventory"
	resourceFederationSummary     = "legator://federation/summary"
	resourceJobsList              = "legator://jobs/list"
	resourceJobsActiveRuns        = "legator://jobs/active-runs"
	resourceGrafanaStatus         = "legator://grafana/status"
	resourceGrafanaSnapshot       = "legator://grafana/snapshot"
	resourceGrafanaCapacityPolicy = "legator://grafana/capacity-policy"
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

	s.server.AddResource(&mcp.Resource{
		URI:         resourceFederationInventory,
		Name:        "Federation Inventory",
		Description: "Federated inventory across source adapters with query/filter support",
		MIMEType:    "application/json",
	}, s.handleFederationInventoryResource)

	s.server.AddResource(&mcp.Resource{
		URI:         resourceFederationSummary,
		Name:        "Federation Summary",
		Description: "Federated source health and aggregate rollups with query/filter support",
		MIMEType:    "application/json",
	}, s.handleFederationSummaryResource)

	s.server.AddResource(&mcp.Resource{
		URI:         resourceJobsList,
		Name:        "Jobs List",
		Description: "Configured scheduled jobs",
		MIMEType:    "application/json",
	}, s.handleJobsListResource)

	s.server.AddResource(&mcp.Resource{
		URI:         resourceJobsActiveRuns,
		Name:        "Jobs Active Runs",
		Description: "Queued/pending/running job runs across all jobs",
		MIMEType:    "application/json",
	}, s.handleJobsActiveRunsResource)

	if s.grafanaClient != nil {
		s.server.AddResource(&mcp.Resource{
			URI:         resourceGrafanaStatus,
			Name:        "Grafana Status",
			Description: "Read-only Grafana adapter status summary",
			MIMEType:    "application/json",
		}, s.handleGrafanaStatusResource)
		s.server.AddResource(&mcp.Resource{
			URI:         resourceGrafanaSnapshot,
			Name:        "Grafana Snapshot",
			Description: "Read-only Grafana capacity snapshot",
			MIMEType:    "application/json",
		}, s.handleGrafanaSnapshotResource)
		s.server.AddResource(&mcp.Resource{
			URI:         resourceGrafanaCapacityPolicy,
			Name:        "Grafana Capacity Policy",
			Description: "Grafana-derived capacity signals and policy rationale projection",
			MIMEType:    "application/json",
		}, s.handleGrafanaCapacityPolicyResource)
	}
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

func (s *MCPServer) handleFederationInventoryResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if s.federationStore == nil {
		return nil, fmt.Errorf("federation store unavailable")
	}
	uri := resourceFederationInventory
	if req != nil && req.Params != nil && req.Params.URI != "" {
		uri = req.Params.URI
	}
	inventory := s.federationStore.Inventory(ctx, federationFilterFromResourceURI(uri))
	return buildJSONResourceResult(req, resourceFederationInventory, inventory)
}

func (s *MCPServer) handleFederationSummaryResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if s.federationStore == nil {
		return nil, fmt.Errorf("federation store unavailable")
	}
	uri := resourceFederationSummary
	if req != nil && req.Params != nil && req.Params.URI != "" {
		uri = req.Params.URI
	}
	summary := s.federationStore.Summary(ctx, federationFilterFromResourceURI(uri))
	return buildJSONResourceResult(req, resourceFederationSummary, summary)
}

func (s *MCPServer) handleJobsListResource(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if s.jobsStore == nil {
		return nil, fmt.Errorf("jobs store unavailable")
	}

	jobsList, err := s.jobsStore.ListJobs()
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(jobsList)
	if err != nil {
		return nil, err
	}

	uri := resourceJobsList
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

func (s *MCPServer) handleJobsActiveRunsResource(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if s.jobsStore == nil {
		return nil, fmt.Errorf("jobs store unavailable")
	}

	jobsList, err := s.jobsStore.ListJobs()
	if err != nil {
		return nil, err
	}

	activeRuns := make([]jobs.JobRun, 0)
	for _, job := range jobsList {
		runs, err := s.jobsStore.ListActiveRunsByJob(strings.TrimSpace(job.ID))
		if err != nil {
			return nil, err
		}
		activeRuns = append(activeRuns, runs...)
	}

	payload := map[string]any{
		"runs":  activeRuns,
		"count": len(activeRuns),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	uri := resourceJobsActiveRuns
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

func (s *MCPServer) handleGrafanaStatusResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if s.grafanaClient == nil {
		return nil, fmt.Errorf("grafana adapter unavailable")
	}
	if err := s.requirePermission(ctx, auth.PermFleetRead); err != nil {
		return nil, err
	}
	status, err := s.grafanaClient.Status(ctx)
	if err != nil {
		return nil, err
	}
	return buildJSONResourceResult(req, resourceGrafanaStatus, map[string]any{"status": status})
}

func (s *MCPServer) handleGrafanaSnapshotResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if s.grafanaClient == nil {
		return nil, fmt.Errorf("grafana adapter unavailable")
	}
	if err := s.requirePermission(ctx, auth.PermFleetRead); err != nil {
		return nil, err
	}
	snapshot, err := s.grafanaClient.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return buildJSONResourceResult(req, resourceGrafanaSnapshot, map[string]any{"snapshot": snapshot})
}

func (s *MCPServer) handleGrafanaCapacityPolicyResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if s.grafanaClient == nil {
		return nil, fmt.Errorf("grafana adapter unavailable")
	}
	if err := s.requirePermission(ctx, auth.PermFleetRead); err != nil {
		return nil, err
	}
	snapshot, err := s.grafanaClient.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	signals := grafanaCapacitySignalsFromSnapshot(snapshot)
	decision := evaluateGrafanaCapacityPolicy(ctx, signals)

	payload := grafanaCapacityPolicyPayload{
		Capacity:        signals,
		PolicyDecision:  decision.Outcome,
		PolicyRationale: decision.Rationale,
	}
	return buildJSONResourceResult(req, resourceGrafanaCapacityPolicy, payload)
}

func federationFilterFromResourceURI(rawURI string) fleet.FederationFilter {
	parsed, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil || parsed == nil {
		return fleet.FederationFilter{}
	}
	q := parsed.Query()
	return fleet.FederationFilter{
		Tag:     strings.TrimSpace(q.Get("tag")),
		Status:  strings.TrimSpace(q.Get("status")),
		Source:  strings.TrimSpace(q.Get("source")),
		Cluster: strings.TrimSpace(q.Get("cluster")),
		Site:    strings.TrimSpace(q.Get("site")),
		Search:  strings.TrimSpace(q.Get("search")),
	}
}

func buildJSONResourceResult(req *mcp.ReadResourceRequest, defaultURI string, payload any) (*mcp.ReadResourceResult, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	uri := defaultURI
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
