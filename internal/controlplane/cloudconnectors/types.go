package cloudconnectors

import "time"

const (
	ProviderAWS   = "aws"
	ProviderGCP   = "gcp"
	ProviderAzure = "azure"

	AuthModeCLI = "cli"

	ScanStatusSuccess = "success"
	ScanStatusError   = "error"
)

// Connector is a configured cloud account/project/subscription source.
type Connector struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Provider   string    `json:"provider"`
	AuthMode   string    `json:"auth_mode"`
	IsEnabled  bool      `json:"is_enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	LastScanAt time.Time `json:"last_scan_at,omitempty"`
	LastStatus string    `json:"last_status,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

// Asset is a normalized discovered resource.
type Asset struct {
	ID           string    `json:"id"`
	ConnectorID  string    `json:"connector_id"`
	Provider     string    `json:"provider"`
	ScopeID      string    `json:"scope_id"`
	Region       string    `json:"region"`
	AssetType    string    `json:"asset_type"`
	AssetID      string    `json:"asset_id"`
	DisplayName  string    `json:"display_name"`
	Status       string    `json:"status"`
	RawJSON      string    `json:"raw_json"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

// AssetFilter controls cloud asset queries.
type AssetFilter struct {
	Provider    string
	ConnectorID string
	Limit       int
}

// ScanResult reports a connector scan outcome.
type ScanResult struct {
	ConnectorID      string    `json:"connector_id"`
	Provider         string    `json:"provider"`
	Status           string    `json:"status"`
	Error            string    `json:"error,omitempty"`
	ScannedAt        time.Time `json:"scanned_at"`
	AssetsDiscovered int       `json:"assets_discovered"`
}

// ScanError is a structured provider scan failure.
type ScanError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func (e *ScanError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Message
}
