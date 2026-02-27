package networkdevices

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Prober defines connectivity + inventory behavior for network devices.
type Prober interface {
	Test(ctx context.Context, device Device, creds CredentialInput) (*TestResult, error)
	Inventory(ctx context.Context, device Device, creds CredentialInput) (*InventoryResult, error)
}

// SSHProber probes devices over SSH.
type SSHProber struct {
	DialTimeout    time.Duration
	CommandTimeout time.Duration
}

func NewSSHProber() *SSHProber {
	return &SSHProber{
		DialTimeout:    5 * time.Second,
		CommandTimeout: 8 * time.Second,
	}
}

func (p *SSHProber) Test(ctx context.Context, device Device, creds CredentialInput) (*TestResult, error) {
	address := net.JoinHostPort(strings.TrimSpace(device.Host), fmt.Sprintf("%d", normalizePort(device.Port)))
	start := time.Now()
	dialer := &net.Dialer{Timeout: p.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return &TestResult{
			DeviceID:  device.ID,
			Address:   address,
			Reachable: false,
			SSHReady:  false,
			LatencyMS: latency,
			Error:     err.Error(),
			Message:   "tcp connection failed",
		}, nil
	}
	_ = conn.Close()

	result := &TestResult{
		DeviceID:  device.ID,
		Address:   address,
		Reachable: true,
		SSHReady:  false,
		LatencyMS: latency,
		Message:   "tcp reachable",
	}

	if strings.TrimSpace(device.Username) == "" {
		return result, nil
	}

	config, err := p.clientConfig(device, creds)
	if err != nil {
		result.Message = "tcp reachable (credentials unavailable for ssh auth test)"
		return result, nil
	}

	client, err := sshDialContext(ctx, "tcp", address, config, p.DialTimeout)
	if err != nil {
		result.Message = "tcp reachable but ssh auth failed"
		result.Error = err.Error()
		return result, nil
	}
	_ = client.Close()

	result.SSHReady = true
	result.Message = "ssh connectivity verified"
	result.Error = ""
	return result, nil
}

func (p *SSHProber) Inventory(ctx context.Context, device Device, creds CredentialInput) (*InventoryResult, error) {
	if strings.TrimSpace(device.Username) == "" {
		return nil, fmt.Errorf("username required for inventory collection")
	}

	config, err := p.clientConfig(device, creds)
	if err != nil {
		return nil, err
	}

	address := net.JoinHostPort(strings.TrimSpace(device.Host), fmt.Sprintf("%d", normalizePort(device.Port)))
	client, err := sshDialContext(ctx, "tcp", address, config, p.DialTimeout)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	commands := vendorCommands(normalizeVendor(device.Vendor))
	result := &InventoryResult{
		DeviceID:    device.ID,
		Vendor:      normalizeVendor(device.Vendor),
		CollectedAt: time.Now().UTC(),
		Raw:         map[string]string{},
		Errors:      []string{},
	}

	for key, command := range commands {
		output, runErr := runCommandWithTimeout(client, command, p.CommandTimeout)
		trimmed := strings.TrimSpace(output)
		if trimmed != "" {
			result.Raw[key] = trimmed
		}
		if runErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %s", key, runErr.Error()))
			continue
		}

		switch key {
		case "hostname":
			result.Hostname = parseHostname(trimmed)
		case "version":
			result.Version = firstLine(trimmed)
		case "interfaces":
			result.Interfaces = parseInterfaces(trimmed)
		}
	}

	if len(result.Errors) == 0 {
		result.Errors = nil
	}
	if len(result.Raw) == 0 {
		result.Raw = nil
	}
	return result, nil
}

func (p *SSHProber) clientConfig(device Device, creds CredentialInput) (*ssh.ClientConfig, error) {
	authMethods := make([]ssh.AuthMethod, 0, 2)
	password := strings.TrimSpace(creds.Password)
	privateKey := strings.TrimSpace(creds.PrivateKey)
	mode := normalizeAuthMode(device.AuthMode)

	if password != "" {
		authMethods = append(authMethods, ssh.Password(password))
	}
	if privateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(privateKey))
		if err != nil {
			return nil, fmt.Errorf("invalid private key")
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if mode == AuthModeAgent {
		if sock := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK")); sock != "" {
			if conn, err := net.Dial("unix", sock); err == nil {
				agentClient := agent.NewClient(conn)
				authMethods = append(authMethods, ssh.PublicKeysCallback(agentClient.Signers))
			}
		}
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no usable ssh credentials provided")
	}

	return &ssh.ClientConfig{
		User:            strings.TrimSpace(device.Username),
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // MVP: inventory target onboarding
		Timeout:         p.DialTimeout,
	}, nil
}

func sshDialContext(ctx context.Context, network, addr string, config *ssh.ClientConfig, timeout time.Duration) (*ssh.Client, error) {
	type result struct {
		client *ssh.Client
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		client, err := ssh.Dial(network, addr, config)
		ch <- result{client: client, err: err}
	}()

	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("ssh dial timeout")
	case out := <-ch:
		return out.client, out.err
	}
}

func runCommandWithTimeout(client *ssh.Client, command string, timeout time.Duration) (string, error) {
	type result struct {
		out string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		session, err := client.NewSession()
		if err != nil {
			ch <- result{"", err}
			return
		}
		defer session.Close()

		output, err := session.CombinedOutput(command)
		ch <- result{out: string(output), err: err}
	}()

	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-timer.C:
		return "", fmt.Errorf("command timeout")
	case out := <-ch:
		return out.out, out.err
	}
}

func vendorCommands(vendor string) map[string]string {
	switch normalizeVendor(vendor) {
	case VendorCisco:
		return map[string]string{
			"hostname":   "show running-config | include ^hostname",
			"version":    "show version | include (Cisco IOS|Version)",
			"interfaces": "show ip interface brief",
		}
	case VendorJunos:
		return map[string]string{
			"hostname":   "show configuration system host-name | display set",
			"version":    "show version | match JUNOS",
			"interfaces": "show interfaces terse",
		}
	case VendorFortinet:
		return map[string]string{
			"hostname":   "get system status | grep Hostname",
			"version":    "get system status | grep Version",
			"interfaces": "get system interface",
		}
	default:
		return map[string]string{
			"hostname":   "hostname",
			"version":    "uname -a",
			"interfaces": "ip -brief link",
		}
	}
}

func parseHostname(raw string) string {
	line := firstLine(raw)
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "hostname")
	line = strings.TrimPrefix(line, "set system host-name")
	line = strings.TrimPrefix(line, "Hostname:")
	line = strings.TrimSpace(strings.Trim(line, ";"))
	if line == "" {
		return firstLine(raw)
	}
	return line
}

func parseInterfaces(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
		if len(out) >= 64 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
