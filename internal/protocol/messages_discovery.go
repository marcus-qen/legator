// Package protocol defines the wire protocol between control plane and probe.
// This file adds lateral-discovery message types.
package protocol

import "time"

// Discovery message types.
const (
	// Probe → Control Plane
	MsgDiscoveryReport MessageType = "discovery_report"
	MsgDeployResult    MessageType = "deploy_result"

	// Control Plane → Probe
	MsgDeployProbe MessageType = "deploy_probe"
)

// DiscoveryReportPayload is sent by a probe to report SSH-reachable hosts
// found on its local network.
type DiscoveryReportPayload struct {
	ProbeID   string           `json:"probe_id"`
	Hosts     []DiscoveredHost `json:"hosts"`
	ScannedAt time.Time        `json:"scanned_at"`
}

// DiscoveredHost represents one reachable host found during lateral scanning.
type DiscoveredHost struct {
	IP          string `json:"ip"`
	Port        int    `json:"port"`
	SSHBanner   string `json:"ssh_banner,omitempty"`
	OSGuess     string `json:"os_guess,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// DeployProbePayload instructs a connected probe to install a probe binary on
// a remote host that was previously approved by the operator.
type DeployProbePayload struct {
	RequestID    string `json:"request_id"`
	CandidateID  string `json:"candidate_id"`
	IP           string `json:"ip"`
	Port         int    `json:"port"`
	SSHUser      string `json:"ssh_user"`
	SSHKey       string `json:"ssh_key"`       // PEM-encoded private key
	BinaryURL    string `json:"binary_url"`    // URL to download probe binary
	ServerURL    string `json:"server_url"`    // control-plane URL for registration
	InstallToken string `json:"install_token"` // single-use registration token
}

// DeployResultPayload is sent back by the deploying probe after attempting
// remote installation.
type DeployResultPayload struct {
	RequestID   string `json:"request_id"`
	CandidateID string `json:"candidate_id"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
}
