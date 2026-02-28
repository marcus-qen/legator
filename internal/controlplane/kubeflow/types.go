package kubeflow

import (
	"context"
	"time"
)

// Client defines the Kubeflow integration boundary used by control-plane surfaces.
type Client interface {
	Status(ctx context.Context) (Status, error)
	Inventory(ctx context.Context) (Inventory, error)
	Refresh(ctx context.Context) (RefreshResult, error)
}

// Status provides a lightweight health and connectivity snapshot.
type Status struct {
	Connected      bool           `json:"connected"`
	Namespace      string         `json:"namespace"`
	Context        string         `json:"context,omitempty"`
	KubectlVersion string         `json:"kubectl_version,omitempty"`
	ServerVersion  string         `json:"server_version,omitempty"`
	CheckedAt      time.Time      `json:"checked_at"`
	Summary        InventoryBrief `json:"summary"`
	Warnings       []string       `json:"warnings,omitempty"`
	LastError      string         `json:"last_error,omitempty"`
}

// Inventory is the normalized read-only resource snapshot returned by Legator.
type Inventory struct {
	Namespace   string             `json:"namespace"`
	Context     string             `json:"context,omitempty"`
	CollectedAt time.Time          `json:"collected_at"`
	Partial     bool               `json:"partial"`
	Warnings    []string           `json:"warnings,omitempty"`
	Counts      map[string]int     `json:"counts"`
	Resources   []ResourceSnapshot `json:"resources"`
}

// InventoryBrief summarizes inventory counts for status and refresh responses.
type InventoryBrief struct {
	Total   int            `json:"total"`
	Counts  map[string]int `json:"counts"`
	Partial bool           `json:"partial"`
}

// ResourceSnapshot captures a minimal, stable resource representation.
type ResourceSnapshot struct {
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Status    string            `json:"status"`
	CreatedAt time.Time         `json:"created_at,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// RefreshResult is returned by the optional guarded refresh action endpoint.
type RefreshResult struct {
	Status    Status    `json:"status"`
	Inventory Inventory `json:"inventory"`
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
