package grafana

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HTTPRequester represents the minimum HTTP client contract used for Grafana reads.
type HTTPRequester interface {
	Do(req *http.Request) (*http.Response, error)
}

// ClientConfig configures the Grafana HTTP client.
type ClientConfig struct {
	BaseURL        string
	APIToken       string
	Timeout        time.Duration
	DashboardLimit int
	TLSSkipVerify  bool
	OrgID          int
	HTTPClient     HTTPRequester
}

// HTTPClient implements Grafana integration through read-only HTTP APIs.
type HTTPClient struct {
	baseURL        string
	apiToken       string
	timeout        time.Duration
	dashboardLimit int
	orgID          int
	httpClient     HTTPRequester
}

// NewHTTPClient builds a Grafana read-only client.
func NewHTTPClient(cfg ClientConfig) *HTTPClient {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	limit := cfg.DashboardLimit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if cfg.TLSSkipVerify {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit opt-in for self-hosted labs
		}
		httpClient = &http.Client{Timeout: timeout, Transport: transport}
	}

	return &HTTPClient{
		baseURL:        baseURL,
		apiToken:       strings.TrimSpace(cfg.APIToken),
		timeout:        timeout,
		dashboardLimit: limit,
		orgID:          cfg.OrgID,
		httpClient:     httpClient,
	}
}

// Status returns a lightweight availability/capacity summary.
func (c *HTTPClient) Status(ctx context.Context) (Status, error) {
	status := Status{CheckedAt: time.Now().UTC(), BaseURL: c.baseURL}

	snapshot, err := c.collectSnapshot(ctx, false)
	if err != nil {
		status.LastError = err.Error()
		return status, err
	}

	status.Connected = true
	status.Health = snapshot.Health
	status.Datasources = snapshot.Datasources
	status.Dashboards = snapshot.Dashboards
	status.Indicators = snapshot.Indicators
	status.Partial = snapshot.Partial
	status.Warnings = cloneSlice(snapshot.Warnings)
	return status, nil
}

// Snapshot returns read-only practical capacity/availability signals.
func (c *HTTPClient) Snapshot(ctx context.Context) (Snapshot, error) {
	return c.collectSnapshot(ctx, true)
}

func (c *HTTPClient) collectSnapshot(ctx context.Context, includeItems bool) (Snapshot, error) {
	snapshot := Snapshot{
		CollectedAt: time.Now().UTC(),
		Datasources: DatasourceSummary{ByType: make(map[string]int)},
		Dashboards:  DashboardSummary{PanelsByDatasource: make(map[string]int)},
	}

	if c.baseURL == "" {
		return snapshot, &ClientError{Code: "config_invalid", Message: "grafana base URL is not configured"}
	}

	health, err := c.health(ctx)
	if err != nil {
		return snapshot, err
	}
	snapshot.Health = health
	if !health.Healthy {
		snapshot.Partial = true
		snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf("grafana health reports database=%q", health.Database))
	}

	datasources, err := c.datasources(ctx)
	if err != nil {
		snapshot.Partial = true
		snapshot.Warnings = append(snapshot.Warnings, "datasources unavailable: "+err.Error())
	} else {
		for _, ds := range datasources {
			snapshot.Datasources.Total++
			if ds.Default {
				snapshot.Datasources.DefaultCount++
			}
			if ds.ReadOnly {
				snapshot.Datasources.ReadOnly++
			}
			dsType := strings.TrimSpace(ds.Type)
			if dsType == "" {
				dsType = "unknown"
			}
			snapshot.Datasources.ByType[dsType]++
		}
		if includeItems {
			snapshot.DatasourceItems = datasources
		}
	}

	dashboards, err := c.dashboardSearch(ctx)
	if err != nil {
		snapshot.Partial = true
		snapshot.Warnings = append(snapshot.Warnings, "dashboard search unavailable: "+err.Error())
	} else {
		snapshot.Dashboards.Total = len(dashboards)
		for i, dash := range dashboards {
			if i >= c.dashboardLimit {
				break
			}

			dashboard, err := c.dashboardByUID(ctx, dash.UID)
			if err != nil {
				snapshot.Partial = true
				snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf("dashboard %s unavailable: %v", dash.UID, err))
				continue
			}

			panelStats := summarizePanels(dashboard.Panels)
			snapshot.Dashboards.Scanned++
			snapshot.Dashboards.Panels += panelStats.Total
			snapshot.Dashboards.QueryBackedPanels += panelStats.QueryBacked
			snapshot.Dashboards.PanelsWithoutDatasource += panelStats.WithoutDatasource
			for datasource, count := range panelStats.ByDatasource {
				snapshot.Dashboards.PanelsByDatasource[datasource] += count
			}

			if includeItems {
				snapshot.DashboardItems = append(snapshot.DashboardItems, DashboardSnapshot{
					UID:               dashboard.UID,
					Title:             dashboard.Title,
					Panels:            panelStats.Total,
					QueryBackedPanels: panelStats.QueryBacked,
				})
			}
		}
	}

	if len(snapshot.Datasources.ByType) == 0 {
		snapshot.Datasources.ByType = nil
	}
	if len(snapshot.Dashboards.PanelsByDatasource) == 0 {
		snapshot.Dashboards.PanelsByDatasource = nil
	}

	snapshot.Warnings = dedupeAndSort(snapshot.Warnings)
	snapshot.Indicators = buildCapacityIndicators(snapshot)
	return snapshot, nil
}

type healthResponse struct {
	Database string `json:"database"`
	Version  string `json:"version"`
	Commit   string `json:"commit"`
}

func (c *HTTPClient) health(ctx context.Context) (ServiceHealthSummary, error) {
	var payload healthResponse
	if err := c.getJSON(ctx, "/api/health", nil, &payload); err != nil {
		return ServiceHealthSummary{}, err
	}
	database := strings.TrimSpace(payload.Database)
	healthy := strings.EqualFold(database, "ok")
	return ServiceHealthSummary{
		Database: database,
		Version:  strings.TrimSpace(payload.Version),
		Commit:   strings.TrimSpace(payload.Commit),
		Healthy:  healthy,
	}, nil
}

type datasourceResponse struct {
	UID       string `json:"uid"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	IsDefault bool   `json:"isDefault"`
	ReadOnly  bool   `json:"readOnly"`
}

func (c *HTTPClient) datasources(ctx context.Context) ([]DatasourceSnapshot, error) {
	var payload []datasourceResponse
	if err := c.getJSON(ctx, "/api/datasources", nil, &payload); err != nil {
		return nil, err
	}

	out := make([]DatasourceSnapshot, 0, len(payload))
	for _, ds := range payload {
		name := strings.TrimSpace(ds.Name)
		if name == "" {
			name = strings.TrimSpace(ds.UID)
		}
		if name == "" {
			continue
		}
		out = append(out, DatasourceSnapshot{
			UID:      strings.TrimSpace(ds.UID),
			Name:     name,
			Type:     strings.TrimSpace(ds.Type),
			Default:  ds.IsDefault,
			ReadOnly: ds.ReadOnly,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

type dashboardSearchResponse struct {
	UID string `json:"uid"`
}

func (c *HTTPClient) dashboardSearch(ctx context.Context) ([]dashboardSearchResponse, error) {
	var payload []dashboardSearchResponse
	query := map[string]string{
		"type":  "dash-db",
		"limit": strconv.Itoa(max(c.dashboardLimit*3, 30)),
	}
	if err := c.getJSON(ctx, "/api/search", query, &payload); err != nil {
		return nil, err
	}
	out := make([]dashboardSearchResponse, 0, len(payload))
	seen := make(map[string]struct{}, len(payload))
	for _, item := range payload {
		uid := strings.TrimSpace(item.UID)
		if uid == "" {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		out = append(out, dashboardSearchResponse{UID: uid})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

type dashboardResponse struct {
	Dashboard struct {
		UID    string           `json:"uid"`
		Title  string           `json:"title"`
		Panels []map[string]any `json:"panels"`
	} `json:"dashboard"`
}

func (c *HTTPClient) dashboardByUID(ctx context.Context, uid string) (DashboardSnapshotPayload, error) {
	var payload dashboardResponse
	if err := c.getJSON(ctx, "/api/dashboards/uid/"+url.PathEscape(uid), nil, &payload); err != nil {
		return DashboardSnapshotPayload{}, err
	}
	title := strings.TrimSpace(payload.Dashboard.Title)
	if title == "" {
		title = uid
	}
	return DashboardSnapshotPayload{
		UID:    strings.TrimSpace(payload.Dashboard.UID),
		Title:  title,
		Panels: payload.Dashboard.Panels,
	}, nil
}

type DashboardSnapshotPayload struct {
	UID    string
	Title  string
	Panels []map[string]any
}

type panelSummary struct {
	Total             int
	QueryBacked       int
	WithoutDatasource int
	ByDatasource      map[string]int
}

func summarizePanels(panels []map[string]any) panelSummary {
	flat := flattenPanels(panels)
	summary := panelSummary{ByDatasource: make(map[string]int)}
	for _, panel := range flat {
		summary.Total++
		ds := panelDatasource(panel)
		if ds == "" {
			summary.WithoutDatasource++
		} else {
			summary.ByDatasource[ds]++
		}
		if panelHasQueryTargets(panel) {
			summary.QueryBacked++
		}
	}
	if len(summary.ByDatasource) == 0 {
		summary.ByDatasource = nil
	}
	return summary
}

func flattenPanels(panels []map[string]any) []map[string]any {
	if len(panels) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(panels))
	for _, panel := range panels {
		if panel == nil {
			continue
		}
		out = append(out, panel)
		nested, ok := panel["panels"]
		if !ok {
			continue
		}
		nestedPanels := coercePanels(nested)
		if len(nestedPanels) == 0 {
			continue
		}
		out = append(out, flattenPanels(nestedPanels)...)
	}
	return out
}

func coercePanels(raw any) []map[string]any {
	switch panels := raw.(type) {
	case []map[string]any:
		return panels
	case []any:
		out := make([]map[string]any, 0, len(panels))
		for _, candidate := range panels {
			if panel, ok := candidate.(map[string]any); ok {
				out = append(out, panel)
			}
		}
		return out
	default:
		return nil
	}
}

func panelDatasource(panel map[string]any) string {
	raw, ok := panel["datasource"]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case map[string]any:
		for _, key := range []string{"uid", "name", "type"} {
			if candidate := strings.TrimSpace(fmt.Sprintf("%v", value[key])); candidate != "" && candidate != "<nil>" {
				return candidate
			}
		}
	}
	return ""
}

func panelHasQueryTargets(panel map[string]any) bool {
	raw, ok := panel["targets"]
	if !ok || raw == nil {
		return false
	}
	targets, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, target := range targets {
		switch t := target.(type) {
		case map[string]any:
			if hasNonEmptyValue(t) {
				return true
			}
		default:
			if strings.TrimSpace(fmt.Sprintf("%v", t)) != "" {
				return true
			}
		}
	}
	return false
}

func hasNonEmptyValue(values map[string]any) bool {
	for _, value := range values {
		switch typed := value.(type) {
		case nil:
			continue
		case string:
			if strings.TrimSpace(typed) != "" {
				return true
			}
		case []any:
			if len(typed) > 0 {
				return true
			}
		case map[string]any:
			if len(typed) > 0 {
				return true
			}
		default:
			if strings.TrimSpace(fmt.Sprintf("%v", typed)) != "" {
				return true
			}
		}
	}
	return false
}

func buildCapacityIndicators(snapshot Snapshot) CapacityIndicators {
	indicators := CapacityIndicators{DatasourceCount: snapshot.Datasources.Total, Availability: "unknown"}

	if snapshot.Dashboards.Total > 0 {
		indicators.DashboardCoverage = float64(snapshot.Dashboards.Scanned) / float64(snapshot.Dashboards.Total)
	}
	if snapshot.Dashboards.Panels > 0 {
		indicators.QueryCoverage = float64(snapshot.Dashboards.QueryBackedPanels) / float64(snapshot.Dashboards.Panels)
	}

	switch {
	case !snapshot.Health.Healthy:
		indicators.Availability = "degraded"
	case snapshot.Datasources.Total == 0:
		indicators.Availability = "insufficient"
	case indicators.QueryCoverage < 0.25 && snapshot.Dashboards.Panels > 0:
		indicators.Availability = "limited"
	default:
		indicators.Availability = "ready"
	}

	return indicators
}

func (c *HTTPClient) getJSON(ctx context.Context, endpoint string, query map[string]string, dst any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx := ctx
	if c.timeout > 0 {
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	reqURL, err := url.Parse(c.baseURL)
	if err != nil {
		return &ClientError{Code: "config_invalid", Message: "invalid grafana base URL", Detail: err.Error()}
	}
	reqURL.Path = path.Join(reqURL.Path, endpoint)
	params := reqURL.Query()
	for key, value := range query {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		params.Set(key, value)
	}
	reqURL.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return &ClientError{Code: "request_failed", Message: "failed to build grafana request", Detail: err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	if c.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
	}
	if c.orgID > 0 {
		req.Header.Set("X-Grafana-Org-Id", strconv.Itoa(c.orgID))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return classifyRequestError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &ClientError{Code: "auth_failed", Message: "grafana authentication failed", Detail: resp.Status}
	}
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &ClientError{Code: "request_failed", Message: "grafana request failed", Detail: strings.TrimSpace(fmt.Sprintf("%s %s", resp.Status, string(body)))}
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return &ClientError{Code: "parse_error", Message: "failed to parse grafana response", Detail: err.Error()}
	}
	return nil
}

func classifyRequestError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &ClientError{Code: "timeout", Message: "grafana request timed out", Detail: err.Error()}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &ClientError{Code: "timeout", Message: "grafana request timed out", Detail: err.Error()}
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if errors.Is(urlErr.Err, context.DeadlineExceeded) {
			return &ClientError{Code: "timeout", Message: "grafana request timed out", Detail: err.Error()}
		}
	}

	return &ClientError{Code: "unreachable", Message: "grafana unreachable", Detail: err.Error()}
}

func dedupeAndSort(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func cloneSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
