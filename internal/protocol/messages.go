// Package protocol defines the wire protocol between control plane and probe.
// Both sides import this package to ensure type safety.
package protocol

import "time"

// MessageType identifies the kind of message on the WebSocket wire.
type MessageType string

const (
	// Probe → Control Plane
	MsgRegister      MessageType = "register"
	MsgHeartbeat     MessageType = "heartbeat"
	MsgInventory     MessageType = "inventory"
	MsgCommandResult MessageType = "command_result"
	MsgError         MessageType = "error"

	// Control Plane → Probe
	MsgRegistered   MessageType = "registered"
	MsgCommand      MessageType = "command"
	MsgPolicyUpdate MessageType = "policy_update"
	MsgPing         MessageType = "ping"
	MsgPong         MessageType = "pong"
	MsgUpdate       MessageType = "update" // Control Plane → Probe: update binary

	// Bidirectional
	MsgOutputChunk MessageType = "output_chunk"
)

// Envelope wraps every message on the wire.
type Envelope struct {
	ID        string      `json:"id"`
	Type      MessageType `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Payload   any         `json:"payload,omitempty"`
	Signature string      `json:"signature,omitempty"` // HMAC for command verification
}

// RegisterPayload is sent by the probe on initial connection.
type RegisterPayload struct {
	Token    string            `json:"token"` // Single-use registration token
	Hostname string            `json:"hostname"`
	OS       string            `json:"os"`
	Arch     string            `json:"arch"`
	Version  string            `json:"version"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// RegisteredPayload is the control plane's response to registration.
type RegisteredPayload struct {
	ProbeID  string `json:"probe_id"`
	APIKey   string `json:"api_key"`   // Long-lived per-probe key
	PolicyID string `json:"policy_id"` // Initial policy assignment
}

// HeartbeatPayload is sent by the probe every 30s.
type HeartbeatPayload struct {
	ProbeID   string     `json:"probe_id"`
	Uptime    int64      `json:"uptime_seconds"`
	Load      [3]float64 `json:"load_avg"`
	MemUsed   uint64     `json:"mem_used_bytes"`
	MemTotal  uint64     `json:"mem_total_bytes"`
	DiskUsed  uint64     `json:"disk_used_bytes"`
	DiskTotal uint64     `json:"disk_total_bytes"`
}

// CapabilityLevel controls what a probe is allowed to do.
type CapabilityLevel string

const (
	CapObserve   CapabilityLevel = "observe"
	CapDiagnose  CapabilityLevel = "diagnose"
	CapRemediate CapabilityLevel = "remediate"
)

// CommandPayload is sent from the control plane to execute on the probe.
type CommandPayload struct {
	RequestID string          `json:"request_id"`
	Command   string          `json:"command"`
	Args      []string        `json:"args,omitempty"`
	Timeout   time.Duration   `json:"timeout"`
	Level     CapabilityLevel `json:"level"`  // Required capability level
	Stream    bool            `json:"stream"` // Stream output vs wait for completion
}

// CommandResultPayload is the probe's response to a command.
type CommandResultPayload struct {
	RequestID string `json:"request_id"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	Duration  int64  `json:"duration_ms"`
	Truncated bool   `json:"truncated"` // Output exceeded max size
}

// InventoryPayload is the probe's full system inventory.
type InventoryPayload struct {
	ProbeID     string            `json:"probe_id"`
	Hostname    string            `json:"hostname"`
	OS          string            `json:"os"`
	Arch        string            `json:"arch"`
	Kernel      string            `json:"kernel"`
	CPUs        int               `json:"cpus"`
	MemTotal    uint64            `json:"mem_total_bytes"`
	DiskTotal   uint64            `json:"disk_total_bytes"`
	Interfaces  []NetInterface    `json:"interfaces,omitempty"`
	Packages    []Package         `json:"packages,omitempty"`
	Services    []Service         `json:"services,omitempty"`
	Users       []User            `json:"users,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	CollectedAt time.Time         `json:"collected_at"`
}

// NetInterface represents a network interface.
type NetInterface struct {
	Name  string   `json:"name"`
	MAC   string   `json:"mac,omitempty"`
	Addrs []string `json:"addrs,omitempty"`
	State string   `json:"state"`
}

// Package represents an installed package.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Manager string `json:"manager"` // apt, yum, apk, etc.
}

// Service represents a system service.
type Service struct {
	Name    string `json:"name"`
	State   string `json:"state"` // running, stopped, failed
	Enabled bool   `json:"enabled"`
}

// User represents a system user.
type User struct {
	Name   string   `json:"name"`
	UID    int      `json:"uid"`
	Groups []string `json:"groups,omitempty"`
	Shell  string   `json:"shell"`
}

// UpdatePayload tells the probe to download and install a new binary.
type UpdatePayload struct {
	URL      string `json:"url"`      // Download URL for new binary
	Checksum string `json:"checksum"` // SHA256 hex digest
	Version  string `json:"version"`  // Target version string
	Restart  bool   `json:"restart"`  // Restart after update
}

// OutputChunkPayload streams incremental output from a running command.
type OutputChunkPayload struct {
	RequestID string `json:"request_id"`
	Stream    string `json:"stream"` // "stdout" or "stderr"
	Data      string `json:"data"`
	Seq       int    `json:"seq"`       // sequence number for ordering
	Final     bool   `json:"final"`     // true = command has finished
	ExitCode  int    `json:"exit_code"` // only meaningful when Final=true
}

// PolicyUpdatePayload pushes a new policy to the probe.
type PolicyUpdatePayload struct {
	PolicyID string          `json:"policy_id"`
	Level    CapabilityLevel `json:"level"`
	Allowed  []string        `json:"allowed,omitempty"` // Command allowlist
	Blocked  []string        `json:"blocked,omitempty"` // Command blocklist
	Paths    []string        `json:"paths,omitempty"`   // Protected paths
}
