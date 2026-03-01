package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/controlplane/events"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/grafana"
	"github.com/marcus-qen/legator/internal/controlplane/jobs"
	"github.com/marcus-qen/legator/internal/controlplane/kubeflow"
	"github.com/marcus-qen/legator/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listProbesInput struct {
	Status string `json:"status,omitempty" jsonschema:"probe status filter: online, offline, or all"`
	Tag    string `json:"tag,omitempty" jsonschema:"optional tag filter"`
}

type probeInfoInput struct {
	ProbeID string `json:"probe_id" jsonschema:"probe identifier"`
}

type runCommandInput struct {
	ProbeID string `json:"probe_id" jsonschema:"probe identifier"`
	Command string `json:"command" jsonschema:"shell command to run"`
}

type fleetQueryInput struct {
	Question string `json:"question" jsonschema:"natural language fleet question"`
}

type federationQueryInput struct {
	Tag     string `json:"tag,omitempty" jsonschema:"optional tag filter"`
	Status  string `json:"status,omitempty" jsonschema:"optional status filter (online/offline/degraded/pending)"`
	Source  string `json:"source,omitempty" jsonschema:"optional source id/name filter"`
	Cluster string `json:"cluster,omitempty" jsonschema:"optional cluster filter"`
	Site    string `json:"site,omitempty" jsonschema:"optional site filter"`
	Search  string `json:"search,omitempty" jsonschema:"optional free-text search across probe/source fields"`
}

type searchAuditInput struct {
	ProbeID string `json:"probe_id,omitempty" jsonschema:"optional probe id filter"`
	Type    string `json:"type,omitempty" jsonschema:"optional audit event type filter"`
	Since   string `json:"since,omitempty" jsonschema:"optional ISO-8601 timestamp filter"`
	Limit   int    `json:"limit,omitempty" jsonschema:"optional limit (default 50)"`
}

type decideApprovalInput struct {
	ApprovalID string `json:"approval_id" jsonschema:"approval request identifier"`
	Decision   string `json:"decision" jsonschema:"approval decision: approved or denied"`
	DecidedBy  string `json:"decided_by" jsonschema:"operator identity recording the decision"`
}

type kubeflowRunStatusInput struct {
	Name      string `json:"name" jsonschema:"run name"`
	Kind      string `json:"kind,omitempty" jsonschema:"optional kubernetes resource kind (default runs.kubeflow.org)"`
	Namespace string `json:"namespace,omitempty" jsonschema:"optional namespace override"`
}

type kubeflowSubmitRunInput struct {
	Name      string          `json:"name,omitempty" jsonschema:"optional run name override"`
	Kind      string          `json:"kind,omitempty" jsonschema:"optional kubernetes resource kind"`
	Namespace string          `json:"namespace,omitempty" jsonschema:"optional namespace override"`
	Manifest  json.RawMessage `json:"manifest" jsonschema:"JSON manifest for run submission"`
}

type kubeflowCancelRunInput struct {
	Name      string `json:"name" jsonschema:"run name"`
	Kind      string `json:"kind,omitempty" jsonschema:"optional kubernetes resource kind (default runs.kubeflow.org)"`
	Namespace string `json:"namespace,omitempty" jsonschema:"optional namespace override"`
}

type grafanaToolInput struct{}

type grafanaCapacityPolicyPayload struct {
	Capacity       coreapprovalpolicy.CapacitySignals             `json:"capacity"`
	PolicyDecision coreapprovalpolicy.CommandPolicyDecisionOutcome `json:"policy_decision"`
	PolicyRationale coreapprovalpolicy.CommandPolicyRationale      `json:"policy_rationale"`
}

type listJobRunsInput struct {
	JobID         string `json:"job_id,omitempty" jsonschema:"optional job identifier filter"`
	ProbeID       string `json:"probe_id,omitempty" jsonschema:"optional probe identifier filter"`
	Status        string `json:"status,omitempty" jsonschema:"optional status filter: queued, pending, running, success, failed, canceled, denied"`
	StartedAfter  string `json:"started_after,omitempty" jsonschema:"optional RFC3339 lower timestamp bound"`
	StartedBefore string `json:"started_before,omitempty" jsonschema:"optional RFC3339 upper timestamp bound"`
	Limit         int    `json:"limit,omitempty" jsonschema:"optional max results (default 50, max 500)"`
}

type getJobRunInput struct {
	RunID string `json:"run_id" jsonschema:"job run identifier"`
}

type pollActiveJobStatusInput struct {
	JobID       string `json:"job_id" jsonschema:"job identifier"`
	WaitMS      int    `json:"wait_ms,omitempty" jsonschema:"optional poll window in milliseconds (default 10000, max 60000)"`
	IntervalMS  int    `json:"interval_ms,omitempty" jsonschema:"optional poll interval in milliseconds (default 500, min 100)"`
	IncludeJob  *bool  `json:"include_job,omitempty" jsonschema:"include job metadata in poll response"`
	IncludeRuns *bool  `json:"include_runs,omitempty" jsonschema:"include active run payloads in poll response (default true)"`
}

type streamJobRunOutputInput struct {
	RunID     string `json:"run_id" jsonschema:"job run identifier"`
	WaitMS    int    `json:"wait_ms,omitempty" jsonschema:"optional max wait for output chunks in milliseconds (default 5000, max 60000)"`
	MaxChunks int    `json:"max_chunks,omitempty" jsonschema:"optional max chunks to collect before returning (default 256)"`
}

type streamJobEventsInput struct {
	JobID       string `json:"job_id,omitempty" jsonschema:"optional job identifier filter"`
	RunID       string `json:"run_id,omitempty" jsonschema:"optional run identifier filter"`
	ExecutionID string `json:"execution_id,omitempty" jsonschema:"optional execution identifier filter"`
	RequestID   string `json:"request_id,omitempty" jsonschema:"optional command request identifier filter"`
	ProbeID     string `json:"probe_id,omitempty" jsonschema:"optional probe identifier filter"`
	WaitMS      int    `json:"wait_ms,omitempty" jsonschema:"optional long-poll timeout for live events in milliseconds"`
	Limit       int    `json:"limit,omitempty" jsonschema:"optional max events to return (default 50)"`
	Since       string `json:"since,omitempty" jsonschema:"optional RFC3339 lower timestamp bound"`
}

type jobRunsSummaryPayload struct {
	Runs          []jobs.JobRun `json:"runs"`
	Count         int           `json:"count"`
	FailedCount   int           `json:"failed_count"`
	SuccessCount  int           `json:"success_count"`
	RunningCount  int           `json:"running_count"`
	PendingCount  int           `json:"pending_count"`
	QueuedCount   int           `json:"queued_count"`
	CanceledCount int           `json:"canceled_count"`
	DeniedCount   int           `json:"denied_count"`
}

type mcpRunSummary struct {
	Queued   int
	Pending  int
	Running  int
	Success  int
	Failed   int
	Canceled int
	Denied   int
}

type pollActiveJobStatusPayload struct {
	JobID      string        `json:"job_id"`
	PolledAt   time.Time     `json:"polled_at"`
	Completed  bool          `json:"completed"`
	Active     bool          `json:"active"`
	ActiveRuns int           `json:"active_runs"`
	Runs       []jobs.JobRun `json:"runs,omitempty"`
	Job        *jobs.Job     `json:"job,omitempty"`
}

type streamJobRunOutputPayload struct {
	RunID          string                        `json:"run_id"`
	RequestID      string                        `json:"request_id"`
	Status         string                        `json:"status"`
	Terminal       bool                          `json:"terminal"`
	Chunks         []protocol.OutputChunkPayload `json:"chunks"`
	BufferedOutput string                        `json:"buffered_output,omitempty"`
}

type streamJobEventsPayload struct {
	Events   []audit.Event `json:"events"`
	Count    int           `json:"count"`
	PolledAt time.Time     `json:"polled_at"`
}

type probeSummary struct {
	ID       string    `json:"id"`
	Hostname string    `json:"hostname"`
	Status   string    `json:"status"`
	Tags     []string  `json:"tags,omitempty"`
	LastSeen time.Time `json:"last_seen"`
}

func (s *MCPServer) registerTools() {
	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_list_probes",
		Description: "List probes in the Legator fleet with status/tag filtering",
	}, s.handleListProbes)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_probe_info",
		Description: "Get detailed state for a specific probe",
	}, s.handleProbeInfo)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_run_command",
		Description: "Run a command on a probe and wait for the result",
	}, s.handleRunCommand)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_get_inventory",
		Description: "Get system inventory for a specific probe",
	}, s.handleGetInventory)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_fleet_query",
		Description: "Answer a natural-language fleet query using summary stats",
	}, s.handleFleetQuery)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_federation_inventory",
		Description: "Get federated inventory across sources with source/cluster/site/tag/status/search filters",
	}, s.handleFederationInventory)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_federation_summary",
		Description: "Get federated source health/aggregate rollups with source/cluster/site/tag/status/search filters",
	}, s.handleFederationSummary)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_search_audit",
		Description: "Search Legator audit events",
	}, s.handleSearchAudit)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_decide_approval",
		Description: "Approve or deny a pending approval request and dispatch on approve",
	}, s.handleDecideApproval)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_probe_health",
		Description: "Get health score/status/warnings for a probe",
	}, s.handleProbeHealth)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_list_jobs",
		Description: "List configured scheduled jobs",
	}, s.handleListJobs)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_list_job_runs",
		Description: "List job runs with optional status/time filters",
	}, s.handleListJobRuns)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_get_job_run",
		Description: "Get details for a specific job run",
	}, s.handleGetJobRun)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_poll_job_active",
		Description: "Poll active status for a scheduled job until terminal or timeout",
	}, s.handlePollActiveJobStatus)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_stream_job_run_output",
		Description: "Stream incremental output chunks for a running job run",
	}, s.handleStreamJobRunOutput)

	mcp.AddTool(s.server, &mcp.Tool{
		Name:        "legator_stream_job_events",
		Description: "Stream or poll job lifecycle events using audit/event bus infrastructure",
	}, s.handleStreamJobEvents)

	if s.grafanaClient != nil {
		mcp.AddTool(s.server, &mcp.Tool{
			Name:        "legator_grafana_status",
			Description: "Get Grafana adapter status (read-only capacity availability summary)",
		}, s.handleGrafanaStatus)
		mcp.AddTool(s.server, &mcp.Tool{
			Name:        "legator_grafana_snapshot",
			Description: "Get Grafana adapter capacity snapshot (read-only)",
		}, s.handleGrafanaSnapshot)
		mcp.AddTool(s.server, &mcp.Tool{
			Name:        "legator_grafana_capacity_policy",
			Description: "Get Grafana-derived capacity signals plus policy rationale projection",
		}, s.handleGrafanaCapacityPolicy)
	}

	if s.kubeflowRunStatus != nil {
		mcp.AddTool(s.server, &mcp.Tool{
			Name:        "legator_kubeflow_run_status",
			Description: "Get Kubeflow run/job status from the control-plane adapter",
		}, s.handleKubeflowRunStatus)
	}
	if s.kubeflowSubmitRun != nil {
		mcp.AddTool(s.server, &mcp.Tool{
			Name:        "legator_kubeflow_submit_run",
			Description: "Submit a Kubeflow run/job manifest through policy gates",
		}, s.handleKubeflowSubmitRun)
	}
	if s.kubeflowCancelRun != nil {
		mcp.AddTool(s.server, &mcp.Tool{
			Name:        "legator_kubeflow_cancel_run",
			Description: "Cancel a Kubeflow run/job through policy gates",
		}, s.handleKubeflowCancelRun)
	}
}

func (s *MCPServer) handleListProbes(_ context.Context, _ *mcp.CallToolRequest, input listProbesInput) (*mcp.CallToolResult, any, error) {
	if s.fleetStore == nil {
		return nil, nil, fmt.Errorf("fleet store unavailable")
	}

	status := strings.ToLower(strings.TrimSpace(input.Status))
	if status == "" {
		status = "all"
	}
	if status != "all" && status != "online" && status != "offline" {
		return nil, nil, fmt.Errorf("invalid status %q: expected online, offline, or all", input.Status)
	}

	var probes []*fleet.ProbeState
	tag := strings.TrimSpace(input.Tag)
	if tag != "" {
		probes = s.fleetStore.ListByTag(tag)
	} else {
		probes = s.fleetStore.List()
	}

	out := make([]probeSummary, 0, len(probes))
	for _, ps := range probes {
		if status != "all" && strings.ToLower(ps.Status) != status {
			continue
		}
		out = append(out, probeSummary{
			ID:       ps.ID,
			Hostname: ps.Hostname,
			Status:   ps.Status,
			Tags:     append([]string(nil), ps.Tags...),
			LastSeen: ps.LastSeen,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})

	return jsonToolResult(out)
}

func (s *MCPServer) handleProbeInfo(_ context.Context, _ *mcp.CallToolRequest, input probeInfoInput) (*mcp.CallToolResult, any, error) {
	if s.fleetStore == nil {
		return nil, nil, fmt.Errorf("fleet store unavailable")
	}
	probeID := strings.TrimSpace(input.ProbeID)
	if probeID == "" {
		return nil, nil, fmt.Errorf("probe_id is required")
	}

	ps, ok := s.fleetStore.Get(probeID)
	if !ok {
		return nil, nil, fmt.Errorf("probe not found: %s", probeID)
	}

	return jsonToolResult(ps)
}

func (s *MCPServer) handleRunCommand(ctx context.Context, _ *mcp.CallToolRequest, input runCommandInput) (*mcp.CallToolResult, any, error) {
	if s.fleetStore == nil {
		return nil, nil, fmt.Errorf("fleet store unavailable")
	}
	if s.dispatcher == nil {
		return nil, nil, fmt.Errorf("command transport unavailable")
	}

	probeID := strings.TrimSpace(input.ProbeID)
	if probeID == "" {
		return nil, nil, fmt.Errorf("probe_id is required")
	}
	command := strings.TrimSpace(input.Command)
	if command == "" {
		return nil, nil, fmt.Errorf("command is required")
	}

	ps, ok := s.fleetStore.Get(probeID)
	if !ok {
		return nil, nil, fmt.Errorf("probe not found: %s", probeID)
	}

	invokeInput := corecommanddispatch.AssembleCommandInvokeMCP(probeID, command, ps.PolicyLevel)
	projection := corecommanddispatch.InvokeCommandForSurface(ctx, invokeInput, s.dispatcher)
	return renderRunCommandMCP(projection)
}

func (s *MCPServer) handleDecideApproval(_ context.Context, _ *mcp.CallToolRequest, input decideApprovalInput) (*mcp.CallToolResult, any, error) {
	if s.decideApproval == nil {
		return nil, nil, fmt.Errorf("approval service unavailable")
	}

	invokeInput, err := coreapprovalpolicy.AssembleDecideApprovalInvokeMCP(input.ApprovalID, input.Decision, input.DecidedBy)
	if err != nil {
		return nil, nil, err
	}

	projection := coreapprovalpolicy.InvokeDecideApproval(invokeInput, s.decideApproval, coreapprovalpolicy.DecideApprovalRenderSurfaceMCP)
	return renderDecideApprovalMCP(projection)
}

func (s *MCPServer) handleKubeflowRunStatus(ctx context.Context, _ *mcp.CallToolRequest, input kubeflowRunStatusInput) (*mcp.CallToolResult, any, error) {
	if s.kubeflowRunStatus == nil {
		return nil, nil, fmt.Errorf("kubeflow adapter unavailable")
	}
	result, err := s.kubeflowRunStatus(ctx, kubeflow.RunStatusRequest{
		Name:      strings.TrimSpace(input.Name),
		Kind:      strings.TrimSpace(input.Kind),
		Namespace: strings.TrimSpace(input.Namespace),
	})
	if err != nil {
		return nil, nil, err
	}
	return jsonToolResult(map[string]any{"run": result})
}

func (s *MCPServer) handleKubeflowSubmitRun(ctx context.Context, _ *mcp.CallToolRequest, input kubeflowSubmitRunInput) (*mcp.CallToolResult, any, error) {
	if s.kubeflowSubmitRun == nil {
		return nil, nil, fmt.Errorf("kubeflow adapter unavailable")
	}
	result, err := s.kubeflowSubmitRun(ctx, kubeflow.SubmitRunRequest{
		Name:      strings.TrimSpace(input.Name),
		Kind:      strings.TrimSpace(input.Kind),
		Namespace: strings.TrimSpace(input.Namespace),
		Manifest:  input.Manifest,
	})
	if err != nil {
		return nil, nil, err
	}
	return jsonToolResult(result)
}

func (s *MCPServer) handleKubeflowCancelRun(ctx context.Context, _ *mcp.CallToolRequest, input kubeflowCancelRunInput) (*mcp.CallToolResult, any, error) {
	if s.kubeflowCancelRun == nil {
		return nil, nil, fmt.Errorf("kubeflow adapter unavailable")
	}
	result, err := s.kubeflowCancelRun(ctx, kubeflow.CancelRunRequest{
		Name:      strings.TrimSpace(input.Name),
		Kind:      strings.TrimSpace(input.Kind),
		Namespace: strings.TrimSpace(input.Namespace),
	})
	if err != nil {
		return nil, nil, err
	}
	return jsonToolResult(result)
}

func (s *MCPServer) handleGrafanaStatus(ctx context.Context, _ *mcp.CallToolRequest, _ grafanaToolInput) (*mcp.CallToolResult, any, error) {
	if s.grafanaClient == nil {
		return nil, nil, fmt.Errorf("grafana adapter unavailable")
	}
	if err := s.requirePermission(ctx, auth.PermFleetRead); err != nil {
		return nil, nil, err
	}
	status, err := s.grafanaClient.Status(ctx)
	if err != nil {
		return nil, nil, err
	}
	return jsonToolResult(map[string]any{"status": status})
}

func (s *MCPServer) handleGrafanaSnapshot(ctx context.Context, _ *mcp.CallToolRequest, _ grafanaToolInput) (*mcp.CallToolResult, any, error) {
	if s.grafanaClient == nil {
		return nil, nil, fmt.Errorf("grafana adapter unavailable")
	}
	if err := s.requirePermission(ctx, auth.PermFleetRead); err != nil {
		return nil, nil, err
	}
	snapshot, err := s.grafanaClient.Snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	return jsonToolResult(map[string]any{"snapshot": snapshot})
}

func (s *MCPServer) handleGrafanaCapacityPolicy(ctx context.Context, _ *mcp.CallToolRequest, _ grafanaToolInput) (*mcp.CallToolResult, any, error) {
	if s.grafanaClient == nil {
		return nil, nil, fmt.Errorf("grafana adapter unavailable")
	}
	if err := s.requirePermission(ctx, auth.PermFleetRead); err != nil {
		return nil, nil, err
	}

	snapshot, err := s.grafanaClient.Snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	signals := grafanaCapacitySignalsFromSnapshot(snapshot)
	decision := evaluateGrafanaCapacityPolicy(ctx, signals)

	payload := grafanaCapacityPolicyPayload{
		Capacity:        signals,
		PolicyDecision:  decision.Outcome,
		PolicyRationale: decision.Rationale,
	}
	return jsonToolResult(payload)
}

func evaluateGrafanaCapacityPolicy(ctx context.Context, signals coreapprovalpolicy.CapacitySignals) coreapprovalpolicy.CommandPolicyDecision {
	svc := coreapprovalpolicy.NewService(
		nil,
		nil,
		nil,
		coreapprovalpolicy.WithCapacitySignalProvider(coreapprovalpolicy.CapacitySignalProviderFunc(func(context.Context) (*coreapprovalpolicy.CapacitySignals, error) {
			clone := signals
			clone.Warnings = append([]string(nil), signals.Warnings...)
			return &clone, nil
		})),
	)
	return svc.EvaluateCommandPolicy(ctx, &protocol.CommandPayload{Command: "echo grafana-capacity"}, protocol.CapObserve)
}

func grafanaCapacitySignalsFromSnapshot(snapshot grafana.Snapshot) coreapprovalpolicy.CapacitySignals {
	return coreapprovalpolicy.CapacitySignals{
		Source:            "grafana",
		Availability:      snapshot.Indicators.Availability,
		DashboardCoverage: snapshot.Indicators.DashboardCoverage,
		QueryCoverage:     snapshot.Indicators.QueryCoverage,
		DatasourceCount:   snapshot.Indicators.DatasourceCount,
		Partial:           snapshot.Partial,
		Warnings:          append([]string(nil), snapshot.Warnings...),
	}
}

func (s *MCPServer) handleGetInventory(_ context.Context, _ *mcp.CallToolRequest, input probeInfoInput) (*mcp.CallToolResult, any, error) {
	if s.fleetStore == nil {
		return nil, nil, fmt.Errorf("fleet store unavailable")
	}
	probeID := strings.TrimSpace(input.ProbeID)
	if probeID == "" {
		return nil, nil, fmt.Errorf("probe_id is required")
	}

	ps, ok := s.fleetStore.Get(probeID)
	if !ok {
		return nil, nil, fmt.Errorf("probe not found: %s", probeID)
	}

	return jsonToolResult(ps.Inventory)
}

func (s *MCPServer) handleFleetQuery(_ context.Context, _ *mcp.CallToolRequest, input fleetQueryInput) (*mcp.CallToolResult, any, error) {
	if s.fleetStore == nil {
		return nil, nil, fmt.Errorf("fleet store unavailable")
	}
	question := strings.TrimSpace(input.Question)
	if question == "" {
		return nil, nil, fmt.Errorf("question is required")
	}

	probes := s.fleetStore.List()
	counts := s.fleetStore.Count()
	tags := s.fleetStore.TagCounts()
	inventory := s.fleetStore.Inventory(fleet.InventoryFilter{})

	tagPairs := make([]string, 0, len(tags))
	for tag, count := range tags {
		tagPairs = append(tagPairs, fmt.Sprintf("%s=%d", tag, count))
	}
	sort.Strings(tagPairs)
	if len(tagPairs) == 0 {
		tagPairs = append(tagPairs, "none")
	}

	text := fmt.Sprintf(
		"Fleet summary for query %q\nTotal probes: %d\nOnline: %d\nOffline: %d\nDegraded: %d\nTag summary: %s\nInventory totals: CPUs=%d RAM=%d bytes",
		question,
		len(probes),
		counts["online"],
		counts["offline"],
		counts["degraded"],
		strings.Join(tagPairs, ", "),
		inventory.Aggregates.TotalCPUs,
		inventory.Aggregates.TotalRAMBytes,
	)

	return textToolResult(text), nil, nil
}

func (s *MCPServer) handleFederationInventory(ctx context.Context, _ *mcp.CallToolRequest, input federationQueryInput) (*mcp.CallToolResult, any, error) {
	if s.federationStore == nil {
		return nil, nil, fmt.Errorf("federation store unavailable")
	}
	filter := federationFilterFromMCPInput(input)
	inventory := s.federationStore.Inventory(ctx, filter)
	return jsonToolResult(inventory)
}

func (s *MCPServer) handleFederationSummary(ctx context.Context, _ *mcp.CallToolRequest, input federationQueryInput) (*mcp.CallToolResult, any, error) {
	if s.federationStore == nil {
		return nil, nil, fmt.Errorf("federation store unavailable")
	}
	filter := federationFilterFromMCPInput(input)
	summary := s.federationStore.Summary(ctx, filter)
	return jsonToolResult(summary)
}

func (s *MCPServer) handleSearchAudit(_ context.Context, _ *mcp.CallToolRequest, input searchAuditInput) (*mcp.CallToolResult, any, error) {
	if s.auditStore == nil {
		return nil, nil, fmt.Errorf("audit store unavailable")
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 50
	}

	filter := audit.Filter{
		ProbeID: strings.TrimSpace(input.ProbeID),
		Type:    audit.EventType(strings.TrimSpace(input.Type)),
		Limit:   limit,
	}

	if sinceRaw := strings.TrimSpace(input.Since); sinceRaw != "" {
		since, err := time.Parse(time.RFC3339, sinceRaw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid since timestamp (expected RFC3339): %w", err)
		}
		filter.Since = since
	}

	events := s.auditStore.Query(filter)
	return jsonToolResult(events)
}

func (s *MCPServer) handleProbeHealth(_ context.Context, _ *mcp.CallToolRequest, input probeInfoInput) (*mcp.CallToolResult, any, error) {
	if s.fleetStore == nil {
		return nil, nil, fmt.Errorf("fleet store unavailable")
	}
	probeID := strings.TrimSpace(input.ProbeID)
	if probeID == "" {
		return nil, nil, fmt.Errorf("probe_id is required")
	}

	ps, ok := s.fleetStore.Get(probeID)
	if !ok {
		return nil, nil, fmt.Errorf("probe not found: %s", probeID)
	}

	health := fleet.HealthScore{Score: 0, Status: "unknown", Warnings: []string{"no health data"}}
	if ps.Health != nil {
		health = *ps.Health
	}
	return jsonToolResult(health)
}

func (s *MCPServer) handleListJobs(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	if s.jobsStore == nil {
		return nil, nil, fmt.Errorf("jobs store unavailable")
	}

	jobsList, err := s.jobsStore.ListJobs()
	if err != nil {
		return nil, nil, err
	}

	return jsonToolResult(jobsList)
}

func (s *MCPServer) handleListJobRuns(_ context.Context, _ *mcp.CallToolRequest, input listJobRunsInput) (*mcp.CallToolResult, any, error) {
	if s.jobsStore == nil {
		return nil, nil, fmt.Errorf("jobs store unavailable")
	}

	query, err := parseMCPJobRunQuery(input)
	if err != nil {
		return nil, nil, err
	}
	if query.JobID != "" {
		if _, err := s.jobsStore.GetJob(query.JobID); err != nil {
			return nil, nil, fmt.Errorf("job not found: %s", query.JobID)
		}
	}

	runs, err := s.jobsStore.ListRuns(query)
	if err != nil {
		return nil, nil, err
	}
	summary := summarizeMCPRuns(runs)

	payload := jobRunsSummaryPayload{
		Runs:          runs,
		Count:         len(runs),
		FailedCount:   summary.Failed,
		SuccessCount:  summary.Success,
		RunningCount:  summary.Running,
		PendingCount:  summary.Pending,
		QueuedCount:   summary.Queued,
		CanceledCount: summary.Canceled,
		DeniedCount:   summary.Denied,
	}
	return jsonToolResult(payload)
}

func (s *MCPServer) handleGetJobRun(_ context.Context, _ *mcp.CallToolRequest, input getJobRunInput) (*mcp.CallToolResult, any, error) {
	if s.jobsStore == nil {
		return nil, nil, fmt.Errorf("jobs store unavailable")
	}
	runID := strings.TrimSpace(input.RunID)
	if runID == "" {
		return nil, nil, fmt.Errorf("run_id is required")
	}

	run, err := s.jobsStore.GetRun(runID)
	if err != nil {
		return nil, nil, fmt.Errorf("run not found: %s", runID)
	}

	active := run.Status == jobs.RunStatusQueued || run.Status == jobs.RunStatusPending || run.Status == jobs.RunStatusRunning
	payload := map[string]any{
		"run":      run,
		"active":   active,
		"terminal": !active,
	}
	return jsonToolResult(payload)
}

func (s *MCPServer) handlePollActiveJobStatus(ctx context.Context, _ *mcp.CallToolRequest, input pollActiveJobStatusInput) (*mcp.CallToolResult, any, error) {
	if s.jobsStore == nil {
		return nil, nil, fmt.Errorf("jobs store unavailable")
	}
	jobID := strings.TrimSpace(input.JobID)
	if jobID == "" {
		return nil, nil, fmt.Errorf("job_id is required")
	}

	job, err := s.jobsStore.GetJob(jobID)
	if err != nil {
		return nil, nil, fmt.Errorf("job not found: %s", jobID)
	}

	waitMS := input.WaitMS
	if waitMS <= 0 {
		waitMS = 10_000
	}
	if waitMS > 60_000 {
		waitMS = 60_000
	}

	intervalMS := input.IntervalMS
	if intervalMS <= 0 {
		intervalMS = 500
	}
	if intervalMS < 100 {
		intervalMS = 100
	}

	includeRuns := true
	if input.IncludeRuns != nil {
		includeRuns = *input.IncludeRuns
	}

	deadline := time.Now().UTC().Add(time.Duration(waitMS) * time.Millisecond)
	for {
		runs, err := s.jobsStore.ListActiveRunsByJob(jobID)
		if err != nil {
			return nil, nil, err
		}

		now := time.Now().UTC()
		payload := pollActiveJobStatusPayload{
			JobID:      jobID,
			PolledAt:   now,
			Completed:  len(runs) == 0,
			Active:     len(runs) > 0,
			ActiveRuns: len(runs),
		}
		if includeRuns {
			payload.Runs = runs
		}
		if input.IncludeJob != nil && *input.IncludeJob {
			freshJob, err := s.jobsStore.GetJob(jobID)
			if err == nil {
				payload.Job = freshJob
			} else {
				payload.Job = job
			}
		}

		if payload.Completed || !now.Before(deadline) {
			return jsonToolResult(payload)
		}

		timer := time.NewTimer(time.Duration(intervalMS) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *MCPServer) handleStreamJobRunOutput(ctx context.Context, _ *mcp.CallToolRequest, input streamJobRunOutputInput) (*mcp.CallToolResult, any, error) {
	if s.jobsStore == nil {
		return nil, nil, fmt.Errorf("jobs store unavailable")
	}
	runID := strings.TrimSpace(input.RunID)
	if runID == "" {
		return nil, nil, fmt.Errorf("run_id is required")
	}

	run, err := s.jobsStore.GetRun(runID)
	if err != nil {
		return nil, nil, fmt.Errorf("run not found: %s", runID)
	}

	waitMS := input.WaitMS
	if waitMS <= 0 {
		waitMS = 5_000
	}
	if waitMS > 60_000 {
		waitMS = 60_000
	}

	maxChunks := input.MaxChunks
	if maxChunks <= 0 {
		maxChunks = 256
	}
	if maxChunks > 1024 {
		maxChunks = 1024
	}

	chunks := make([]protocol.OutputChunkPayload, 0, maxChunks)
	terminal := isTerminalRunStatus(run.Status)

	requestID := strings.TrimSpace(run.RequestID)
	if requestID != "" && s.hub != nil && !terminal {
		sub, cleanup := s.hub.SubscribeStream(requestID, maxChunks)
		defer cleanup()

		timer := time.NewTimer(time.Duration(waitMS) * time.Millisecond)
		defer timer.Stop()

		collecting := true
		for collecting {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-timer.C:
				collecting = false
			case chunk, ok := <-sub.Ch:
				if !ok {
					collecting = false
					continue
				}
				chunks = append(chunks, chunk)
				if chunk.Final || len(chunks) >= maxChunks {
					collecting = false
				}
			}
		}
	}

	if refreshedRun, err := s.jobsStore.GetRun(runID); err == nil {
		run = refreshedRun
	}

	payload := streamJobRunOutputPayload{
		RunID:          run.ID,
		RequestID:      run.RequestID,
		Status:         run.Status,
		Terminal:       isTerminalRunStatus(run.Status),
		Chunks:         chunks,
		BufferedOutput: run.Output,
	}
	return jsonToolResult(payload)
}

func (s *MCPServer) handleStreamJobEvents(ctx context.Context, _ *mcp.CallToolRequest, input streamJobEventsInput) (*mcp.CallToolResult, any, error) {
	limit := input.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	var since time.Time
	if strings.TrimSpace(input.Since) != "" {
		parsed, err := parseRFC3339MCP(input.Since)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid since timestamp: %w", err)
		}
		since = parsed
	}

	collected := make([]audit.Event, 0, limit)
	if s.auditStore != nil {
		eventsFromAudit := s.auditStore.Query(audit.Filter{Since: since, Limit: limit})
		for _, evt := range eventsFromAudit {
			if !isJobLifecycleAuditEvent(evt.Type) {
				continue
			}
			if !matchesJobAuditEvent(evt, input) {
				continue
			}
			collected = append(collected, evt)
			if len(collected) >= limit {
				break
			}
		}
	}

	waitMS := input.WaitMS
	if waitMS < 0 {
		waitMS = 0
	}
	if waitMS > 60_000 {
		waitMS = 60_000
	}

	if len(collected) == 0 && waitMS > 0 && s.eventBus != nil {
		subID := fmt.Sprintf("mcp-job-events-%d", time.Now().UnixNano())
		ch := s.eventBus.Subscribe(subID)
		defer s.eventBus.Unsubscribe(subID)

		timer := time.NewTimer(time.Duration(waitMS) * time.Millisecond)
		defer timer.Stop()

		for len(collected) < limit {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-timer.C:
				payload := streamJobEventsPayload{Events: collected, Count: len(collected), PolledAt: time.Now().UTC()}
				return jsonToolResult(payload)
			case evt, ok := <-ch:
				if !ok {
					payload := streamJobEventsPayload{Events: collected, Count: len(collected), PolledAt: time.Now().UTC()}
					return jsonToolResult(payload)
				}
				if !isJobLifecycleBusEvent(evt.Type) {
					continue
				}

				auditEvt := audit.Event{
					Timestamp: evt.Timestamp,
					Type:      audit.EventType(evt.Type),
					ProbeID:   evt.ProbeID,
					Summary:   evt.Summary,
					Detail:    evt.Detail,
				}
				if !matchesJobAuditEvent(auditEvt, input) {
					continue
				}
				collected = append(collected, auditEvt)
			}
		}
	}

	payload := streamJobEventsPayload{Events: collected, Count: len(collected), PolledAt: time.Now().UTC()}
	return jsonToolResult(payload)
}

func federationFilterFromMCPInput(input federationQueryInput) fleet.FederationFilter {
	return fleet.FederationFilter{
		Tag:     strings.TrimSpace(input.Tag),
		Status:  strings.TrimSpace(input.Status),
		Source:  strings.TrimSpace(input.Source),
		Cluster: strings.TrimSpace(input.Cluster),
		Site:    strings.TrimSpace(input.Site),
		Search:  strings.TrimSpace(input.Search),
	}
}

func parseMCPJobRunQuery(input listJobRunsInput) (jobs.RunQuery, error) {
	query := jobs.RunQuery{
		JobID:   strings.TrimSpace(input.JobID),
		ProbeID: strings.TrimSpace(input.ProbeID),
		Limit:   input.Limit,
	}
	if input.Limit < 0 {
		return jobs.RunQuery{}, fmt.Errorf("limit must be a positive integer")
	}

	status := strings.TrimSpace(input.Status)
	if status != "" {
		switch status {
		case jobs.RunStatusQueued, jobs.RunStatusPending, jobs.RunStatusRunning, jobs.RunStatusSuccess, jobs.RunStatusFailed, jobs.RunStatusCanceled, jobs.RunStatusDenied:
			query.Status = status
		default:
			return jobs.RunQuery{}, fmt.Errorf("status must be one of: queued, pending, running, success, failed, canceled, denied")
		}
	}

	if raw := strings.TrimSpace(input.StartedAfter); raw != "" {
		ts, err := parseRFC3339MCP(raw)
		if err != nil {
			return jobs.RunQuery{}, fmt.Errorf("started_after must be RFC3339")
		}
		query.StartedAfter = &ts
	}
	if raw := strings.TrimSpace(input.StartedBefore); raw != "" {
		ts, err := parseRFC3339MCP(raw)
		if err != nil {
			return jobs.RunQuery{}, fmt.Errorf("started_before must be RFC3339")
		}
		query.StartedBefore = &ts
	}
	if query.StartedAfter != nil && query.StartedBefore != nil && query.StartedAfter.After(*query.StartedBefore) {
		return jobs.RunQuery{}, fmt.Errorf("started_after must be <= started_before")
	}

	return query, nil
}

func parseRFC3339MCP(raw string) (time.Time, error) {
	if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return ts.UTC(), nil
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, err
	}
	return ts.UTC(), nil
}

func summarizeMCPRuns(runs []jobs.JobRun) mcpRunSummary {
	summary := mcpRunSummary{}
	for _, run := range runs {
		switch run.Status {
		case jobs.RunStatusQueued:
			summary.Queued++
		case jobs.RunStatusPending:
			summary.Pending++
		case jobs.RunStatusRunning:
			summary.Running++
		case jobs.RunStatusSuccess:
			summary.Success++
		case jobs.RunStatusFailed:
			summary.Failed++
		case jobs.RunStatusCanceled:
			summary.Canceled++
		case jobs.RunStatusDenied:
			summary.Denied++
		}
	}
	return summary
}

func isTerminalRunStatus(status string) bool {
	switch status {
	case jobs.RunStatusSuccess, jobs.RunStatusFailed, jobs.RunStatusCanceled, jobs.RunStatusDenied:
		return true
	default:
		return false
	}
}

func isJobLifecycleAuditEvent(typ audit.EventType) bool {
	switch typ {
	case audit.EventJobCreated,
		audit.EventJobUpdated,
		audit.EventJobDeleted,
		audit.EventJobRunAdmissionAllowed,
		audit.EventJobRunAdmissionQueued,
		audit.EventJobRunAdmissionDenied,
		audit.EventJobRunQueued,
		audit.EventJobRunStarted,
		audit.EventJobRunRetryScheduled,
		audit.EventJobRunSucceeded,
		audit.EventJobRunFailed,
		audit.EventJobRunCanceled,
		audit.EventJobRunDenied:
		return true
	default:
		return false
	}
}

func isJobLifecycleBusEvent(typ events.EventType) bool {
	switch typ {
	case events.JobCreated,
		events.JobUpdated,
		events.JobDeleted,
		events.JobRunAdmissionAllowed,
		events.JobRunAdmissionQueued,
		events.JobRunAdmissionDenied,
		events.JobRunQueued,
		events.JobRunStarted,
		events.JobRunRetryScheduled,
		events.JobRunSucceeded,
		events.JobRunFailed,
		events.JobRunCanceled,
		events.JobRunDenied:
		return true
	default:
		return false
	}
}

func matchesJobAuditEvent(evt audit.Event, filter streamJobEventsInput) bool {
	if strings.TrimSpace(filter.ProbeID) != "" && strings.TrimSpace(evt.ProbeID) != strings.TrimSpace(filter.ProbeID) {
		return false
	}
	if strings.TrimSpace(filter.JobID) != "" && detailField(evt.Detail, "job_id") != strings.TrimSpace(filter.JobID) {
		return false
	}
	if strings.TrimSpace(filter.RunID) != "" && detailField(evt.Detail, "run_id") != strings.TrimSpace(filter.RunID) {
		return false
	}
	if strings.TrimSpace(filter.ExecutionID) != "" && detailField(evt.Detail, "execution_id") != strings.TrimSpace(filter.ExecutionID) {
		return false
	}
	if strings.TrimSpace(filter.RequestID) != "" && detailField(evt.Detail, "request_id") != strings.TrimSpace(filter.RequestID) {
		return false
	}
	return true
}

func detailField(detail any, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}

	switch typed := detail.(type) {
	case map[string]any:
		if value, ok := typed[key]; ok {
			return fmt.Sprintf("%v", value)
		}
	case map[string]string:
		return typed[key]
	}
	return ""
}

func jsonToolResult(v any) (*mcp.CallToolResult, any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return textToolResult(string(data)), nil, nil
}

func textToolResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}
