/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// HeadscaleNode represents a node from the Headscale API.
type HeadscaleNode struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	GivenName  string   `json:"givenName"`
	IPAddresses []string `json:"ipAddresses"`
	Online     bool     `json:"online"`
	LastSeen   string   `json:"lastSeen"`
	User       HeadscaleUser `json:"user"`
	ForcedTags []string `json:"forcedTags"`
}

// HeadscaleUser is the owner of a Headscale node.
type HeadscaleUser struct {
	Name string `json:"name"`
}

// HeadscaleNodesResponse is the response from the Headscale nodes API.
type HeadscaleNodesResponse struct {
	Nodes []HeadscaleNode `json:"nodes"`
}

// HeadscaleSyncConfig configures the Headscale synchronizer.
type HeadscaleSyncConfig struct {
	// BaseURL is the Headscale API URL (e.g., https://headscale.example.com).
	BaseURL string

	// APIKey is the Headscale API key.
	APIKey string

	// SyncInterval is how often to poll for node changes.
	SyncInterval time.Duration
}

// SyncStatus summarizes the health and freshness of the Headscale sync loop.
type SyncStatus struct {
	Provider            string     `json:"provider"`
	BaseURL             string     `json:"baseUrl,omitempty"`
	SyncInterval        string     `json:"syncInterval"`
	DeviceCount         int        `json:"deviceCount"`
	LastAttempt         *time.Time `json:"lastAttempt,omitempty"`
	LastSuccess         *time.Time `json:"lastSuccess,omitempty"`
	LastError           string     `json:"lastError,omitempty"`
	ConsecutiveFailures int        `json:"consecutiveFailures"`
	TotalSyncs          int        `json:"totalSyncs"`
	TotalFailures       int        `json:"totalFailures"`
	Healthy             bool       `json:"healthy"`
}

// HeadscaleSync synchronizes Headscale nodes to the device inventory.
type HeadscaleSync struct {
	config  HeadscaleSyncConfig
	client  *http.Client
	log     logr.Logger
	mu      sync.RWMutex
	devices map[string]*ManagedDevice // keyed by node name

	lastAttempt         *time.Time
	lastSuccess         *time.Time
	lastError           string
	consecutiveFailures int
	totalSyncs          int
	totalFailures       int
}

// NewHeadscaleSync creates a new Headscale synchronizer.
func NewHeadscaleSync(cfg HeadscaleSyncConfig, log logr.Logger) *HeadscaleSync {
	if cfg.SyncInterval == 0 {
		cfg.SyncInterval = 30 * time.Second
	}

	return &HeadscaleSync{
		config:  cfg,
		client:  &http.Client{Timeout: 10 * time.Second},
		log:     log.WithName("headscale-sync"),
		devices: make(map[string]*ManagedDevice),
	}
}

// Start runs the periodic Headscale synchronization loop.
// Implements controller-runtime's manager.Runnable interface.
func (h *HeadscaleSync) Start(ctx context.Context) error {
	h.log.Info("Headscale sync loop starting",
		"baseURL", h.config.BaseURL,
		"interval", h.config.SyncInterval.String(),
	)

	// Initial sync (best-effort).
	if err := h.Sync(ctx); err != nil {
		h.log.Error(err, "Initial Headscale sync failed")
	}

	ticker := time.NewTicker(h.config.SyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.log.Info("Headscale sync loop stopping")
			return nil
		case <-ticker.C:
			if err := h.Sync(ctx); err != nil {
				h.log.Error(err, "Headscale sync failed")
			}
		}
	}
}

// NeedLeaderElection ensures only the elected manager performs inventory sync.
func (h *HeadscaleSync) NeedLeaderElection() bool {
	return true
}

// Sync performs one synchronization cycle: fetch nodes from Headscale, update inventory.
func (h *HeadscaleSync) Sync(ctx context.Context) error {
	attempt := time.Now().UTC()
	nodes, err := h.fetchNodes(ctx)

	h.mu.Lock()
	h.totalSyncs++
	h.lastAttempt = &attempt

	if err != nil {
		h.totalFailures++
		h.consecutiveFailures++
		h.lastError = err.Error()
		h.mu.Unlock()
		return fmt.Errorf("failed to fetch Headscale nodes: %w", err)
	}

	h.consecutiveFailures = 0
	h.lastError = ""
	now := time.Now().UTC()
	h.lastSuccess = &now

	// Track which nodes are still present
	seen := make(map[string]bool)

	for _, node := range nodes {
		name := node.GivenName
		if name == "" {
			name = node.Name
		}
		seen[name] = true

		existing, exists := h.devices[name]
		if !exists {
			// New device discovered
			device := h.nodeToDevice(node)
			h.devices[name] = device
			h.log.Info("New device discovered via Headscale",
				"name", name,
				"online", node.Online,
				"ips", node.IPAddresses,
			)
		} else {
			// Update existing
			existing.Connectivity.Online = node.Online
			if node.Online {
				now := time.Now()
				existing.Connectivity.LastSeen = &now
			}
			if len(node.IPAddresses) > 0 {
				existing.Addresses.Headscale = node.IPAddresses[0]
			}
		}
	}

	// Mark missing nodes as unreachable
	for name, device := range h.devices {
		if !seen[name] && device.Connectivity.Method == ConnectHeadscale {
			device.Connectivity.Online = false
			device.Health.Status = HealthUnreachable
		}
	}

	inventorySize := len(h.devices)
	h.mu.Unlock()

	h.log.Info("Headscale sync completed",
		"totalNodes", len(nodes),
		"inventorySize", inventorySize,
	)

	return nil
}

// Devices returns a snapshot of all known devices.
func (h *HeadscaleSync) Devices() []ManagedDevice {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make([]ManagedDevice, 0, len(h.devices))
	for _, d := range h.devices {
		result = append(result, *d)
	}
	return result
}

// GetDevice returns a single device by name.
func (h *HeadscaleSync) GetDevice(name string) (*ManagedDevice, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	d, ok := h.devices[name]
	if !ok {
		return nil, false
	}
	copy := *d
	return &copy, true
}

// DeviceCount returns the number of known devices.
func (h *HeadscaleSync) DeviceCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.devices)
}

// Status returns sync health/freshness metadata for API consumers.
func (h *HeadscaleSync) Status() SyncStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()

	status := SyncStatus{
		Provider:            "headscale",
		BaseURL:             h.config.BaseURL,
		SyncInterval:        h.config.SyncInterval.String(),
		DeviceCount:         len(h.devices),
		ConsecutiveFailures: h.consecutiveFailures,
		TotalSyncs:          h.totalSyncs,
		TotalFailures:       h.totalFailures,
		Healthy:             h.consecutiveFailures == 0,
	}
	if h.lastAttempt != nil {
		t := *h.lastAttempt
		status.LastAttempt = &t
	}
	if h.lastSuccess != nil {
		t := *h.lastSuccess
		status.LastSuccess = &t
	}
	if h.lastError != "" {
		status.LastError = h.lastError
	}
	return status
}

// InventoryStatus returns sync health/freshness metadata as an API-friendly map.
func (h *HeadscaleSync) InventoryStatus() map[string]any {
	s := h.Status()
	return map[string]any{
		"provider":            s.Provider,
		"baseUrl":             s.BaseURL,
		"syncInterval":        s.SyncInterval,
		"deviceCount":         s.DeviceCount,
		"lastAttempt":         s.LastAttempt,
		"lastSuccess":         s.LastSuccess,
		"lastError":           s.LastError,
		"consecutiveFailures": s.ConsecutiveFailures,
		"totalSyncs":          s.TotalSyncs,
		"totalFailures":       s.TotalFailures,
		"healthy":             s.Healthy,
	}
}

// fetchNodes calls the Headscale API to list all nodes.
func (h *HeadscaleSync) fetchNodes(ctx context.Context) ([]HeadscaleNode, error) {
	url := h.config.BaseURL + "/api/v1/node"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+h.config.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("Headscale API returned %d: %s", resp.StatusCode, string(body))
	}

	var result HeadscaleNodesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return result.Nodes, nil
}

// nodeToDevice converts a Headscale node to a ManagedDevice.
func (h *HeadscaleSync) nodeToDevice(node HeadscaleNode) *ManagedDevice {
	name := node.GivenName
	if name == "" {
		name = node.Name
	}

	device := &ManagedDevice{
		Name:     name,
		Hostname: node.Name,
		Type:     DeviceTypeUnknown,
		Connectivity: DeviceConnectivity{
			Method: ConnectHeadscale,
			NodeID: node.ID,
			Online: node.Online,
		},
		Health: DeviceHealth{
			Status: HealthUnknown,
		},
	}

	if len(node.IPAddresses) > 0 {
		device.Addresses.Headscale = node.IPAddresses[0]
	}

	if node.Online {
		now := time.Now()
		device.Connectivity.LastSeen = &now
		device.Health.Status = HealthHealthy // Assume healthy if online
	}

	// Convert Headscale tags to device tags (strip "tag:" prefix)
	for _, tag := range node.ForcedTags {
		if len(tag) > 4 && tag[:4] == "tag:" {
			device.Tags = append(device.Tags, tag[4:])
		} else {
			device.Tags = append(device.Tags, tag)
		}
	}

	return device
}
