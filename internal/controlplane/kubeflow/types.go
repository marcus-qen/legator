package kubeflow

import (
	"context"
	"encoding/json"
	"time"
)

const (
	// DefaultRunResource is the Kubernetes resource used when no kind is supplied.
	DefaultRunResource = "runs.kubeflow.org"
)

// Client defines the Kubeflow integration boundary used by control-plane surfaces.
type Client interface {
	Status(ctx context.Context) (Status, error)
	Inventory(ctx context.Context) (Inventory, error)
	Refresh(ctx context.Context) (RefreshResult, error)
	RunStatus(ctx context.Context, request RunStatusRequest) (RunStatusResult, error)
	SubmitRun(ctx context.Context, request SubmitRunRequest) (SubmitRunResult, error)
	CancelRun(ctx context.Context, request CancelRunRequest) (CancelRunResult, error)
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

// RunStatusRequest identifies a Kubeflow run-like resource.
type RunStatusRequest struct {
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// SubmitRunRequest accepts a manifest for a run/job submission.
type SubmitRunRequest struct {
	Kind      string          `json:"kind,omitempty"`
	Name      string          `json:"name,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
	Manifest  json.RawMessage `json:"manifest"`
}

// CancelRunRequest identifies a run-like resource to cancel.
type CancelRunRequest struct {
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// RunStatusResult is a normalized run status snapshot.
type RunStatusResult struct {
	Kind       string            `json:"kind"`
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace"`
	Status     string            `json:"status"`
	Message    string            `json:"message,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	ObservedAt time.Time         `json:"observed_at"`
}

// StatusTransition captures before/after status movement for a mutation.
type StatusTransition struct {
	Action     string    `json:"action"`
	Before     string    `json:"before,omitempty"`
	After      string    `json:"after"`
	Changed    bool      `json:"changed"`
	ObservedAt time.Time `json:"observed_at"`
}

// SubmitRunResult represents the outcome for a submit mutation.
type SubmitRunResult struct {
	Run         RunStatusResult  `json:"run"`
	Transition  StatusTransition `json:"transition"`
	SubmittedAt time.Time        `json:"submitted_at"`
}

// CancelRunResult represents the outcome for a cancel mutation.
type CancelRunResult struct {
	Run        RunStatusResult  `json:"run"`
	Transition StatusTransition `json:"transition"`
	Canceled   bool             `json:"canceled"`
	CanceledAt time.Time        `json:"canceled_at"`
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
