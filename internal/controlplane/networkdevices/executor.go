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

const (
	defaultExecutorDialTimeout    = 10 * time.Second
	defaultExecutorCommandTimeout = 30 * time.Second
	defaultMaxOutputBytes         = 64 * 1024 // 64 KB
)

// SSHExecutor runs commands on network devices via SSH.
// It integrates with the Store for per-device credential lookup.
type SSHExecutor struct {
	DialTimeout    time.Duration
	CommandTimeout time.Duration
	MaxOutputBytes int
	store          *Store
}

// NewSSHExecutor creates an SSHExecutor backed by the given store for credential lookup.
func NewSSHExecutor(store *Store) *SSHExecutor {
	return &SSHExecutor{
		DialTimeout:    defaultExecutorDialTimeout,
		CommandTimeout: defaultExecutorCommandTimeout,
		MaxOutputBytes: defaultMaxOutputBytes,
		store:          store,
	}
}

// Execute runs a single command on the device and returns the output.
// If no credentials are provided inline, they are looked up from the store.
func (e *SSHExecutor) Execute(ctx context.Context, device Device, creds CredentialInput, command string) (*CommandResult, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}

	creds = e.resolveCredentials(device.ID, creds)

	dialTimeout := e.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = defaultExecutorDialTimeout
	}
	cmdTimeout := e.CommandTimeout
	if cmdTimeout <= 0 {
		cmdTimeout = defaultExecutorCommandTimeout
	}

	config, err := buildSSHClientConfig(device, creds, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("ssh config: %w", err)
	}

	address := net.JoinHostPort(strings.TrimSpace(device.Host), fmt.Sprintf("%d", normalizePort(device.Port)))
	client, err := sshDialContext(ctx, "tcp", address, config, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("ssh connect %s: %w", address, err)
	}
	defer client.Close()

	start := time.Now()
	raw, runErr := runCommandWithTimeout(client, command, cmdTimeout)
	elapsed := time.Since(start)

	maxBytes := e.MaxOutputBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxOutputBytes
	}

	truncated := false
	if len(raw) > maxBytes {
		raw = raw[:maxBytes]
		truncated = true
	}

	result := &CommandResult{
		DeviceID:   device.ID,
		Command:    command,
		Output:     raw,
		Truncated:  truncated,
		DurationMS: elapsed.Milliseconds(),
		ExecutedAt: time.Now().UTC(),
	}
	if runErr != nil {
		result.Error = runErr.Error()
	}
	return result, nil
}

// resolveCredentials merges stored credentials if none are provided inline.
func (e *SSHExecutor) resolveCredentials(deviceID string, provided CredentialInput) CredentialInput {
	if strings.TrimSpace(provided.Password) != "" || strings.TrimSpace(provided.PrivateKey) != "" {
		return provided
	}
	if e.store == nil {
		return provided
	}
	stored, err := e.store.GetCredential(deviceID)
	if err != nil || stored == nil {
		return provided
	}
	return CredentialInput{
		Password:   stored.Password,
		PrivateKey: stored.PrivateKey,
	}
}

// buildSSHClientConfig constructs an *ssh.ClientConfig for a device + credentials.
func buildSSHClientConfig(device Device, creds CredentialInput, dialTimeout time.Duration) (*ssh.ClientConfig, error) {
	authMethods := make([]ssh.AuthMethod, 0, 3)

	if pw := strings.TrimSpace(creds.Password); pw != "" {
		authMethods = append(authMethods, ssh.Password(pw))
	}
	if key := strings.TrimSpace(creds.PrivateKey); key != "" {
		signer, err := ssh.ParsePrivateKey([]byte(key))
		if err != nil {
			return nil, fmt.Errorf("invalid private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if normalizeAuthMode(device.AuthMode) == AuthModeAgent {
		if sock := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK")); sock != "" {
			if conn, err := net.Dial("unix", sock); err == nil {
				agentClient := agent.NewClient(conn)
				authMethods = append(authMethods, ssh.PublicKeysCallback(agentClient.Signers))
			}
		}
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no usable ssh credentials")
	}

	timeout := dialTimeout
	if timeout <= 0 {
		timeout = defaultExecutorDialTimeout
	}
	return &ssh.ClientConfig{
		User:            strings.TrimSpace(device.Username),
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // network device probing
		Timeout:         timeout,
	}, nil
}
