package fleet

import (
	"fmt"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

const (
	ProbeTypeAgent  = "agent"
	ProbeTypeRemote = "remote"
)

// RemoteProbeConfig defines the SSH endpoint metadata for a remote probe.
type RemoteProbeConfig struct {
	Host          string `json:"host"`
	Port          int    `json:"port,omitempty"`
	Username      string `json:"username"`
	AuthMode      string `json:"auth_mode,omitempty"`
	HasPassword   bool   `json:"has_password,omitempty"`
	HasPrivateKey bool   `json:"has_private_key,omitempty"`
}

// RemoteProbeCredentials stores SSH auth material for a remote probe.
// These fields are persisted but never exposed in API JSON.
type RemoteProbeCredentials struct {
	Password   string `json:"-"`
	PrivateKey string `json:"-"`
}

// RemoteProbeRegistration captures data needed to register a remote probe.
type RemoteProbeRegistration struct {
	ID          string
	Hostname    string
	OS          string
	Arch        string
	Tags        []string
	TenantID    string
	PolicyLevel protocol.CapabilityLevel
	Remote      RemoteProbeConfig
	Credentials RemoteProbeCredentials
}

func normalizeRemotePort(port int) int {
	if port <= 0 {
		return 22
	}
	return port
}

func normalizeProbeType(probeType string) string {
	probeType = strings.ToLower(strings.TrimSpace(probeType))
	if probeType == "" {
		return ProbeTypeAgent
	}
	return probeType
}

func (m *Manager) RegisterRemote(spec RemoteProbeRegistration) (*ProbeState, error) {
	id := strings.TrimSpace(spec.ID)
	if id == "" {
		return nil, fmt.Errorf("remote probe id is required")
	}

	host := strings.TrimSpace(spec.Remote.Host)
	username := strings.TrimSpace(spec.Remote.Username)
	if host == "" {
		return nil, fmt.Errorf("remote host is required")
	}
	if username == "" {
		return nil, fmt.Errorf("remote username is required")
	}

	password := strings.TrimSpace(spec.Credentials.Password)
	privateKey := strings.TrimSpace(spec.Credentials.PrivateKey)
	if password == "" && privateKey == "" {
		return nil, fmt.Errorf("remote probe requires password or private key")
	}

	remote := RemoteProbeConfig{
		Host:          host,
		Port:          normalizeRemotePort(spec.Remote.Port),
		Username:      username,
		AuthMode:      strings.TrimSpace(spec.Remote.AuthMode),
		HasPassword:   password != "",
		HasPrivateKey: privateKey != "",
	}

	hostname := strings.TrimSpace(spec.Hostname)
	if hostname == "" {
		hostname = remote.Host
	}

	level := spec.PolicyLevel
	if level == "" {
		level = protocol.CapObserve
	}

	now := time.Now().UTC()
	ps := &ProbeState{
		ID:                id,
		Hostname:          hostname,
		OS:                strings.TrimSpace(spec.OS),
		Arch:              strings.TrimSpace(spec.Arch),
		Status:            "pending",
		Type:              ProbeTypeRemote,
		PolicyLevel:       level,
		Registered:        now,
		LastSeen:          now,
		Labels:            map[string]string{},
		Tags:              normalizeTags(spec.Tags),
		TenantID:          strings.TrimSpace(spec.TenantID),
		Remote:            &remote,
		RemoteCredentials: &RemoteProbeCredentials{Password: password, PrivateKey: privateKey},
	}

	m.mu.Lock()
	m.probes[id] = ps
	m.mu.Unlock()

	m.logger.Info("remote probe registered",
		zap.String("id", id),
		zap.String("host", remote.Host),
		zap.Int("port", remote.Port),
		zap.String("username", remote.Username),
	)

	return ps, nil
}

func (m *Manager) ListRemote() []*ProbeState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*ProbeState, 0)
	for _, ps := range m.probes {
		if normalizeProbeType(ps.Type) == ProbeTypeRemote {
			result = append(result, ps)
		}
	}
	return result
}

func (m *Manager) SetStatus(id, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ps, ok := m.probes[id]
	if !ok {
		return fmt.Errorf("unknown probe: %s", id)
	}

	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		return fmt.Errorf("status is required")
	}

	ps.Status = status
	if status == "online" || status == "degraded" {
		ps.LastSeen = time.Now().UTC()
	}
	return nil
}
