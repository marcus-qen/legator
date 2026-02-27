package discovery

import "time"

const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"

	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"

	MaxHostsPerScan = 256
	MaxPrefixRange  = 24
)

// ScanRun represents a single discovery scan execution.
type ScanRun struct {
	ID          int64      `json:"id"`
	CIDR        string     `json:"cidr"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Status      string     `json:"status"`
	Error       string     `json:"error,omitempty"`
}

// Candidate is a possible Linux host for probe installation.
type Candidate struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	IP         string `json:"ip"`
	Hostname   string `json:"hostname,omitempty"`
	OpenPorts  []int  `json:"open_ports"`
	Confidence string `json:"confidence"`
}

// ScanResponse is returned by POST /api/v1/discovery/scan and run detail endpoint.
type ScanResponse struct {
	Run        ScanRun     `json:"run"`
	Candidates []Candidate `json:"candidates"`
}

// InstallTokenResponse contains registration-assist output.
type InstallTokenResponse struct {
	Token              string    `json:"token"`
	ExpiresAt          time.Time `json:"expires_at"`
	InstallCommand     string    `json:"install_command"`
	SSHExampleTemplate string    `json:"ssh_example_template"`
}
