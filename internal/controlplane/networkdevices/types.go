package networkdevices

import "time"

const (
	VendorCisco    = "cisco"
	VendorJunos    = "junos"
	VendorFortinet = "fortinet"
	VendorGeneric  = "generic"

	AuthModePassword = "password"
	AuthModeAgent    = "agent"
	AuthModeKey      = "key"
)

// Device is a managed network target.
type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	Vendor    string    `json:"vendor"`
	Username  string    `json:"username"`
	AuthMode  string    `json:"auth_mode"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CredentialInput is accepted by test/inventory endpoints but never persisted.
type CredentialInput struct {
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}

// TestResult reports safe connectivity test output.
type TestResult struct {
	DeviceID  string `json:"device_id"`
	Address   string `json:"address"`
	Reachable bool   `json:"reachable"`
	SSHReady  bool   `json:"ssh_ready"`
	LatencyMS int64  `json:"latency_ms"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
}

// InventoryResult reports best-effort inventory collection.
type InventoryResult struct {
	DeviceID    string            `json:"device_id"`
	Vendor      string            `json:"vendor"`
	CollectedAt time.Time         `json:"collected_at"`
	Hostname    string            `json:"hostname,omitempty"`
	Version     string            `json:"version,omitempty"`
	Serial      string            `json:"serial,omitempty"`
	Interfaces  []string          `json:"interfaces,omitempty"`
	Raw         map[string]string `json:"raw,omitempty"`
	Errors      []string          `json:"errors,omitempty"`
}

// CommandRequest is the request body for ad-hoc command execution.
type CommandRequest struct {
	Command    string `json:"command"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}

// CommandResult holds the output of a single executed command.
type CommandResult struct {
	DeviceID   string    `json:"device_id"`
	Command    string    `json:"command"`
	Output     string    `json:"output"`
	Truncated  bool      `json:"truncated,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	ExecutedAt time.Time `json:"executed_at"`
	Error      string    `json:"error,omitempty"`
}

// DeviceCredential holds stored SSH credentials for a device.
type DeviceCredential struct {
	DeviceID   string    `json:"-"`
	Password   string    `json:"password,omitempty"`
	PrivateKey string    `json:"private_key,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ScanConfig holds optional parameters for an inventory scan.
type ScanConfig struct {
	IncludeRouting bool `json:"include_routing,omitempty"`
}
