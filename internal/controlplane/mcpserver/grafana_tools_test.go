package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	"github.com/marcus-qen/legator/internal/controlplane/grafana"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type stubGrafanaClient struct {
	status        grafana.Status
	snapshot      grafana.Snapshot
	statusErr     error
	snapshotErr   error
	statusCalls   int
	snapshotCalls int
}

func (c *stubGrafanaClient) Status(context.Context) (grafana.Status, error) {
	c.statusCalls++
	if c.statusErr != nil {
		return grafana.Status{}, c.statusErr
	}
	return c.status, nil
}

func (c *stubGrafanaClient) Snapshot(context.Context) (grafana.Snapshot, error) {
	c.snapshotCalls++
	if c.snapshotErr != nil {
		return grafana.Snapshot{}, c.snapshotErr
	}
	return c.snapshot, nil
}

func TestGrafanaToolsAndResourcesRegisteredWithOption(t *testing.T) {
	srv, _, _, _ := newTestMCPServerWithOptions(t, WithGrafanaClient(newStubGrafanaClient()))
	session := connectClient(t, srv)

	toolsResult, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	toolNames := make([]string, 0, len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		toolNames = append(toolNames, tool.Name)
	}
	sort.Strings(toolNames)

	for _, expected := range []string{"legator_grafana_capacity_policy", "legator_grafana_snapshot", "legator_grafana_status"} {
		if !containsString(toolNames, expected) {
			t.Fatalf("expected tool %s in %v", expected, toolNames)
		}
	}

	resourcesResult, err := session.ListResources(context.Background(), &mcp.ListResourcesParams{})
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	resourceURIs := make([]string, 0, len(resourcesResult.Resources))
	for _, resource := range resourcesResult.Resources {
		resourceURIs = append(resourceURIs, resource.URI)
	}
	sort.Strings(resourceURIs)

	for _, expected := range []string{resourceGrafanaCapacityPolicy, resourceGrafanaSnapshot, resourceGrafanaStatus} {
		if !containsString(resourceURIs, expected) {
			t.Fatalf("expected resource %s in %v", expected, resourceURIs)
		}
	}
}

func TestGrafanaStatusAndSnapshotToolsParityWithHTTP(t *testing.T) {
	client := newStubGrafanaClient()
	srv, _, _, _ := newTestMCPServerWithOptions(t, WithGrafanaClient(client))
	session := connectClient(t, srv)

	statusResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "legator_grafana_status"})
	if err != nil {
		t.Fatalf("call legator_grafana_status: %v", err)
	}
	var statusMCP map[string]any
	decodeToolJSON(t, statusResult, &statusMCP)

	handler := grafana.NewHandler(client)
	statusRR := httptest.NewRecorder()
	handler.HandleStatus(statusRR, httptest.NewRequest(http.MethodGet, "/api/v1/grafana/status", nil))
	if statusRR.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 for status, got %d body=%s", statusRR.Code, statusRR.Body.String())
	}
	var statusHTTP map[string]any
	if err := json.Unmarshal(statusRR.Body.Bytes(), &statusHTTP); err != nil {
		t.Fatalf("decode http status payload: %v", err)
	}
	if !reflect.DeepEqual(statusMCP, statusHTTP) {
		t.Fatalf("status payload mismatch:\nmcp=%v\nhttp=%v", statusMCP, statusHTTP)
	}

	snapshotResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "legator_grafana_snapshot"})
	if err != nil {
		t.Fatalf("call legator_grafana_snapshot: %v", err)
	}
	var snapshotMCP map[string]any
	decodeToolJSON(t, snapshotResult, &snapshotMCP)

	snapshotRR := httptest.NewRecorder()
	handler.HandleSnapshot(snapshotRR, httptest.NewRequest(http.MethodGet, "/api/v1/grafana/snapshot", nil))
	if snapshotRR.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 for snapshot, got %d body=%s", snapshotRR.Code, snapshotRR.Body.String())
	}
	var snapshotHTTP map[string]any
	if err := json.Unmarshal(snapshotRR.Body.Bytes(), &snapshotHTTP); err != nil {
		t.Fatalf("decode http snapshot payload: %v", err)
	}
	if !reflect.DeepEqual(snapshotMCP, snapshotHTTP) {
		t.Fatalf("snapshot payload mismatch:\nmcp=%v\nhttp=%v", snapshotMCP, snapshotHTTP)
	}
}

func TestGrafanaCapacityPolicyToolAndResourceRationaleParity(t *testing.T) {
	client := newStubGrafanaClient()
	srv, _, _, _ := newTestMCPServerWithOptions(t, WithGrafanaClient(client))
	session := connectClient(t, srv)

	toolResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "legator_grafana_capacity_policy"})
	if err != nil {
		t.Fatalf("call legator_grafana_capacity_policy: %v", err)
	}
	var toolPayload grafanaCapacityPolicyPayload
	decodeToolJSON(t, toolResult, &toolPayload)

	expectedDecision := evaluateGrafanaCapacityPolicy(context.Background(), grafanaCapacitySignalsFromSnapshot(client.snapshot))
	if toolPayload.PolicyDecision != expectedDecision.Outcome {
		t.Fatalf("unexpected policy_decision: got=%s want=%s", toolPayload.PolicyDecision, expectedDecision.Outcome)
	}
	if toolPayload.PolicyRationale.Policy == "" {
		t.Fatal("expected policy_rationale.policy to be set")
	}
	if toolPayload.PolicyRationale.Capacity == nil {
		t.Fatal("expected policy_rationale.capacity to be present")
	}
	if !reflect.DeepEqual(toolPayload.PolicyRationale, expectedDecision.Rationale) {
		t.Fatalf("policy rationale mismatch:\ngot=%+v\nwant=%+v", toolPayload.PolicyRationale, expectedDecision.Rationale)
	}
	if !reflect.DeepEqual(toolPayload.Capacity, *toolPayload.PolicyRationale.Capacity) {
		t.Fatalf("expected top-level capacity to match policy_rationale.capacity: top=%+v rationale=%+v", toolPayload.Capacity, toolPayload.PolicyRationale.Capacity)
	}

	resourceResult, err := srv.handleGrafanaCapacityPolicyResource(context.Background(), &mcp.ReadResourceRequest{Params: &mcp.ReadResourceParams{URI: resourceGrafanaCapacityPolicy}})
	if err != nil {
		t.Fatalf("read capacity policy resource: %v", err)
	}
	var resourcePayload grafanaCapacityPolicyPayload
	decodeResourceJSON(t, resourceResult, &resourcePayload)
	if !reflect.DeepEqual(resourcePayload, toolPayload) {
		t.Fatalf("tool/resource payload mismatch:\ntool=%+v\nresource=%+v", toolPayload, resourcePayload)
	}
}

func TestGrafanaMCPPermissionCoverage(t *testing.T) {
	client := newStubGrafanaClient()
	deniedErr := errors.New("insufficient permissions (required: fleet:read)")
	requestedPerms := make([]auth.Permission, 0, 6)
	srv, _, _, _ := newTestMCPServerWithOptions(
		t,
		WithGrafanaClient(client),
		WithPermissionChecker(func(_ context.Context, perm auth.Permission) error {
			requestedPerms = append(requestedPerms, perm)
			return deniedErr
		}),
	)

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{name: "tool status", call: func() error {
			_, _, err := srv.handleGrafanaStatus(context.Background(), nil, grafanaToolInput{})
			return err
		}},
		{name: "tool snapshot", call: func() error {
			_, _, err := srv.handleGrafanaSnapshot(context.Background(), nil, grafanaToolInput{})
			return err
		}},
		{name: "tool capacity policy", call: func() error {
			_, _, err := srv.handleGrafanaCapacityPolicy(context.Background(), nil, grafanaToolInput{})
			return err
		}},
		{name: "resource status", call: func() error {
			_, err := srv.handleGrafanaStatusResource(context.Background(), nil)
			return err
		}},
		{name: "resource snapshot", call: func() error {
			_, err := srv.handleGrafanaSnapshotResource(context.Background(), nil)
			return err
		}},
		{name: "resource capacity policy", call: func() error {
			_, err := srv.handleGrafanaCapacityPolicyResource(context.Background(), nil)
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

	if client.statusCalls != 0 || client.snapshotCalls != 0 {
		t.Fatalf("expected permission denial before grafana client calls, statusCalls=%d snapshotCalls=%d", client.statusCalls, client.snapshotCalls)
	}
	if len(requestedPerms) != 6 {
		t.Fatalf("expected 6 permission checks, got %d (%v)", len(requestedPerms), requestedPerms)
	}
	for _, perm := range requestedPerms {
		if perm != auth.PermFleetRead {
			t.Fatalf("expected fleet:read permission check, got %s", perm)
		}
	}
}

func decodeResourceJSON(t *testing.T, result *mcp.ReadResourceResult, out any) {
	t.Helper()
	if result == nil || len(result.Contents) == 0 {
		t.Fatalf("empty resource result: %#v", result)
	}
	if err := json.Unmarshal([]byte(result.Contents[0].Text), out); err != nil {
		t.Fatalf("decode resource json: %v (text=%q)", err, result.Contents[0].Text)
	}
}

func newStubGrafanaClient() *stubGrafanaClient {
	checkedAt := time.Date(2026, time.February, 28, 10, 0, 0, 0, time.UTC)
	collectedAt := checkedAt.Add(2 * time.Minute)

	return &stubGrafanaClient{
		status: grafana.Status{
			Connected: true,
			BaseURL:   "https://grafana.lab.local",
			CheckedAt: checkedAt,
			Health: grafana.ServiceHealthSummary{
				Database: "ok",
				Version:  "10.4.2",
				Commit:   "abc123",
				Healthy:  true,
			},
			Datasources: grafana.DatasourceSummary{
				Total:        3,
				DefaultCount: 1,
				ReadOnly:     1,
				ByType: map[string]int{
					"loki":       1,
					"prometheus": 2,
				},
			},
			Dashboards: grafana.DashboardSummary{
				Total:                   7,
				Scanned:                 5,
				Panels:                  18,
				QueryBackedPanels:       16,
				PanelsWithoutDatasource: 2,
				PanelsByDatasource: map[string]int{
					"loki":       3,
					"prometheus": 13,
				},
			},
			Indicators: grafana.CapacityIndicators{
				Availability:      "limited",
				DashboardCoverage: 0.72,
				QueryCoverage:     0.88,
				DatasourceCount:   3,
			},
			Warnings: []string{"dashboard scan capped at configured limit"},
			Partial:  true,
		},
		snapshot: grafana.Snapshot{
			CollectedAt: collectedAt,
			Health: grafana.ServiceHealthSummary{
				Database: "ok",
				Version:  "10.4.2",
				Commit:   "abc123",
				Healthy:  true,
			},
			Datasources: grafana.DatasourceSummary{
				Total:        3,
				DefaultCount: 1,
				ReadOnly:     1,
				ByType: map[string]int{
					"loki":       1,
					"prometheus": 2,
				},
			},
			Dashboards: grafana.DashboardSummary{
				Total:                   7,
				Scanned:                 5,
				Panels:                  18,
				QueryBackedPanels:       16,
				PanelsWithoutDatasource: 2,
				PanelsByDatasource: map[string]int{
					"loki":       3,
					"prometheus": 13,
				},
			},
			Indicators: grafana.CapacityIndicators{
				Availability:      "limited",
				DashboardCoverage: 0.72,
				QueryCoverage:     0.88,
				DatasourceCount:   3,
			},
			DatasourceItems: []grafana.DatasourceSnapshot{
				{Name: "Prometheus", Type: "prometheus", Default: true, ReadOnly: false},
				{Name: "Loki", Type: "loki", Default: false, ReadOnly: true},
			},
			DashboardItems: []grafana.DashboardSnapshot{
				{UID: "dash-a", Title: "Cluster Overview", Panels: 10, QueryBackedPanels: 9},
				{UID: "dash-b", Title: "Node Health", Panels: 8, QueryBackedPanels: 7},
			},
			Warnings: []string{"dashboard scan capped at configured limit"},
			Partial:  true,
		},
	}
}

func TestGrafanaCapacityPolicyPayloadImplementsRationaleSchema(t *testing.T) {
	client := newStubGrafanaClient()
	signals := grafanaCapacitySignalsFromSnapshot(client.snapshot)
	decision := evaluateGrafanaCapacityPolicy(context.Background(), signals)
	payload := grafanaCapacityPolicyPayload{
		Capacity:        signals,
		PolicyDecision:  decision.Outcome,
		PolicyRationale: decision.Rationale,
	}

	if payload.PolicyRationale.Thresholds.MinDatasourceCount == 0 {
		t.Fatal("expected policy_rationale.thresholds.min_datasource_count")
	}
	if payload.PolicyRationale.Capacity == nil {
		t.Fatal("expected policy_rationale.capacity")
	}
	if len(payload.PolicyRationale.Indicators) == 0 {
		t.Fatal("expected policy_rationale.indicators")
	}
	indicatorNames := make(map[string]struct{}, len(payload.PolicyRationale.Indicators))
	for _, indicator := range payload.PolicyRationale.Indicators {
		indicatorNames[strings.TrimSpace(indicator.Name)] = struct{}{}
	}
	for _, required := range []string{"command_risk", "availability", "datasource_count"} {
		if _, ok := indicatorNames[required]; !ok {
			t.Fatalf("expected rationale indicator %q in %+v", required, payload.PolicyRationale.Indicators)
		}
	}
	if payload.PolicyDecision != coreapprovalpolicy.CommandPolicyDecisionQueue {
		t.Fatalf("expected limited capacity to queue, got %s", payload.PolicyDecision)
	}
}
