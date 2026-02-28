package grafana

import (
	"context"
	"time"
)

// Client defines the Grafana integration boundary used by control-plane surfaces.
type Client interface {
	Status(ctx context.Context) (Status, error)
	Snapshot(ctx context.Context) (Snapshot, error)
}

// Status provides a lightweight availability summary suitable for health checks.
type Status struct {
	Connected   bool                 `json:"connected"`
	BaseURL     string               `json:"base_url,omitempty"`
	CheckedAt   time.Time            `json:"checked_at"`
	Health      ServiceHealthSummary `json:"health"`
	Datasources DatasourceSummary    `json:"datasources"`
	Dashboards  DashboardSummary     `json:"dashboards"`
	Indicators  CapacityIndicators   `json:"indicators"`
	Warnings    []string             `json:"warnings,omitempty"`
	Partial     bool                 `json:"partial"`
	LastError   string               `json:"last_error,omitempty"`
}

// Snapshot captures a read-only capacity/availability view from Grafana.
type Snapshot struct {
	CollectedAt     time.Time            `json:"collected_at"`
	Health          ServiceHealthSummary `json:"health"`
	Datasources     DatasourceSummary    `json:"datasources"`
	Dashboards      DashboardSummary     `json:"dashboards"`
	Indicators      CapacityIndicators   `json:"indicators"`
	DatasourceItems []DatasourceSnapshot `json:"datasource_items,omitempty"`
	DashboardItems  []DashboardSnapshot  `json:"dashboard_items,omitempty"`
	Warnings        []string             `json:"warnings,omitempty"`
	Partial         bool                 `json:"partial"`
}

// ServiceHealthSummary mirrors Grafana's service health signal.
type ServiceHealthSummary struct {
	Database string `json:"database"`
	Version  string `json:"version,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Healthy  bool   `json:"healthy"`
}

// DatasourceSummary provides datasource-level capacity metadata.
type DatasourceSummary struct {
	Total        int            `json:"total"`
	DefaultCount int            `json:"default_count"`
	ReadOnly     int            `json:"read_only"`
	ByType       map[string]int `json:"by_type,omitempty"`
}

// DatasourceSnapshot is the stable read-only representation for a Grafana datasource.
type DatasourceSnapshot struct {
	UID      string `json:"uid,omitempty"`
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Default  bool   `json:"default"`
	ReadOnly bool   `json:"read_only"`
}

// DashboardSummary captures high-level dashboard/panel/query coverage.
type DashboardSummary struct {
	Total                   int            `json:"total"`
	Scanned                 int            `json:"scanned"`
	Panels                  int            `json:"panels"`
	QueryBackedPanels       int            `json:"query_backed_panels"`
	PanelsWithoutDatasource int            `json:"panels_without_datasource"`
	PanelsByDatasource      map[string]int `json:"panels_by_datasource,omitempty"`
}

// DashboardSnapshot captures one dashboard's query/capacity relevant panel stats.
type DashboardSnapshot struct {
	UID               string `json:"uid"`
	Title             string `json:"title"`
	Panels            int    `json:"panels"`
	QueryBackedPanels int    `json:"query_backed_panels"`
}

// CapacityIndicators distills snapshot fields into policy-friendly metrics.
type CapacityIndicators struct {
	Availability      string  `json:"availability"`
	DashboardCoverage float64 `json:"dashboard_coverage"`
	QueryCoverage     float64 `json:"query_coverage"`
	DatasourceCount   int     `json:"datasource_count"`
}

// ClientError exposes categorized adapter failures for API mapping.
type ClientError struct {
	Code    string
	Message string
	Detail  string
}

func (e *ClientError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail == "" {
		return e.Message
	}
	return e.Message + ": " + e.Detail
}
