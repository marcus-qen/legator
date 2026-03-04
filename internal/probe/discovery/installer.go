package discovery

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	defaultDialTimeout    = 30 * time.Second
	defaultInstallTimeout = 5 * time.Minute
	remoteInstallDir      = "/tmp/legator-install"
	remoteBinaryName      = "legator-probe"
)

// InstallTarget holds everything needed to install a probe on a remote host.
type InstallTarget struct {
	// CandidateID is echoed back in the result for correlation.
	CandidateID string
	// IP and Port identify the remote host.
	IP   string
	Port int
	// SSHUser is the username to authenticate with.
	SSHUser string
	// SSHKey is a PEM-encoded RSA/Ed25519 private key.
	SSHKey string
	// BinaryPath is the local filesystem path to the probe binary to upload.
	BinaryPath string
	// ServerURL is passed to the probe for control-plane registration.
	ServerURL string
	// InstallToken is the single-use token for probe registration.
	InstallToken string
}

// InstallResult contains the outcome of a remote installation.
type InstallResult struct {
	CandidateID string `json:"candidate_id"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
}

// SSHDialer is an interface for establishing SSH connections (allows testing).
type SSHDialer interface {
	Dial(ctx context.Context, addr string, config *ssh.ClientConfig) (SSHSession, error)
}

// SSHSession abstracts an active SSH connection.
type SSHSession interface {
	RunCommand(cmd string) (string, error)
	UploadFile(data []byte, remotePath string, mode os.FileMode) error
	Close() error
}

// Installer handles SSH-based remote probe installation.
type Installer struct {
	// DialTimeout overrides the default SSH connection timeout.
	DialTimeout time.Duration
	// InstallTimeout overrides the total installation timeout.
	InstallTimeout time.Duration
	// Dialer overrides the default SSH dialer (for testing).
	Dialer SSHDialer
}

// NewInstaller creates an Installer with sensible defaults.
func NewInstaller() *Installer {
	return &Installer{
		DialTimeout:    defaultDialTimeout,
		InstallTimeout: defaultInstallTimeout,
	}
}

// Install connects to the target via SSH, uploads the probe binary, sets up
// a systemd service, and starts it.
func (inst *Installer) Install(ctx context.Context, target InstallTarget) (*InstallResult, error) {
	result := &InstallResult{CandidateID: target.CandidateID}

	timeout := inst.InstallTimeout
	if timeout <= 0 {
		timeout = defaultInstallTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialer := inst.Dialer
	if dialer == nil {
		dialer = &defaultSSHDialer{dialTimeout: inst.DialTimeout}
	}

	signer, err := parsePrivateKey(target.SSHKey)
	if err != nil {
		result.Error = fmt.Sprintf("parse ssh key: %v", err)
		return result, nil
	}

	user := target.SSHUser
	if user == "" {
		user = "root"
	}
	port := target.Port
	if port == 0 {
		port = 22
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // fleet-install context
		Timeout:         inst.DialTimeout,
	}

	addr := net.JoinHostPort(target.IP, fmt.Sprintf("%d", port))
	session, err := dialer.Dial(ctx, addr, cfg)
	if err != nil {
		result.Error = fmt.Sprintf("ssh dial: %v", err)
		return result, nil
	}
	defer session.Close()

	if err := inst.runInstall(ctx, session, target); err != nil {
		result.Error = err.Error()
		return result, nil
	}

	result.Success = true
	return result, nil
}

func (inst *Installer) runInstall(ctx context.Context, sess SSHSession, target InstallTarget) error {
	// 1. Create install directory
	if _, err := sess.RunCommand(fmt.Sprintf("mkdir -p %s", remoteInstallDir)); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// 2. Upload probe binary if a local path was provided
	remoteBin := filepath.Join(remoteInstallDir, remoteBinaryName)
	if target.BinaryPath != "" {
		data, err := os.ReadFile(target.BinaryPath)
		if err != nil {
			return fmt.Errorf("read binary: %w", err)
		}
		if err := sess.UploadFile(data, remoteBin, 0755); err != nil {
			return fmt.Errorf("upload binary: %w", err)
		}
	} else {
		// No local binary: rely on the control-plane's curl-install script
		if target.ServerURL == "" || target.InstallToken == "" {
			return fmt.Errorf("either binary_path or (server_url + install_token) must be provided")
		}
		installCmd := fmt.Sprintf(
			"curl -sSL %s/install.sh | sudo bash -s -- --server %s --token %s",
			target.ServerURL, target.ServerURL, target.InstallToken,
		)
		if _, err := sess.RunCommand(installCmd); err != nil {
			return fmt.Errorf("curl install: %w", err)
		}
		return nil
	}

	// 3. Write systemd unit
	unitContent := buildSystemdUnit(remoteBin, target.ServerURL, target.InstallToken)
	unitPath := "/etc/systemd/system/legator-probe.service"
	if err := sess.UploadFile([]byte(unitContent), unitPath, 0644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}

	// 4. Enable and start
	cmds := []string{
		"systemctl daemon-reload",
		"systemctl enable legator-probe.service",
		"systemctl start legator-probe.service",
	}
	for _, cmd := range cmds {
		if _, err := sess.RunCommand(cmd); err != nil {
			return fmt.Errorf("%s: %w", cmd, err)
		}
	}

	return nil
}

func buildSystemdUnit(binaryPath, serverURL, installToken string) string {
	var sb strings.Builder
	sb.WriteString("[Unit]\n")
	sb.WriteString("Description=Legator Probe\n")
	sb.WriteString("After=network.target\n\n")
	sb.WriteString("[Service]\n")
	sb.WriteString("Type=simple\n")
	sb.WriteString("Restart=on-failure\n")
	sb.WriteString("RestartSec=10\n")
	sb.WriteString(fmt.Sprintf("ExecStart=%s --server %s --token %s\n",
		binaryPath, serverURL, installToken))
	sb.WriteString("\n[Install]\n")
	sb.WriteString("WantedBy=multi-user.target\n")
	return sb.String()
}

func parsePrivateKey(pemData string) (ssh.Signer, error) {
	if strings.TrimSpace(pemData) == "" {
		return nil, fmt.Errorf("ssh key is empty")
	}
	signer, err := ssh.ParsePrivateKey([]byte(pemData))
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return signer, nil
}

// ── default SSH dialer ────────────────────────────────────────────────────────

type defaultSSHDialer struct {
	dialTimeout time.Duration
}

func (d *defaultSSHDialer) Dial(ctx context.Context, addr string, config *ssh.ClientConfig) (SSHSession, error) {
	timeout := d.dialTimeout
	if timeout <= 0 {
		timeout = defaultDialTimeout
	}

	// Use a goroutine so we can respect ctx cancellation.
	type result struct {
		client *ssh.Client
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := ssh.Dial("tcp", addr, config)
		ch <- result{c, err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return &realSSHSession{client: r.client}, nil
	}
}

// ── real SSH session ──────────────────────────────────────────────────────────

type realSSHSession struct {
	client *ssh.Client
}

func (rs *realSSHSession) RunCommand(cmd string) (string, error) {
	sess, err := rs.client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()

	out, err := sess.CombinedOutput(cmd)
	return string(out), err
}

func (rs *realSSHSession) UploadFile(data []byte, remotePath string, mode os.FileMode) error {
	sess, err := rs.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	// Use SCP receive protocol.
	w, err := sess.StdinPipe()
	if err != nil {
		return err
	}

	name := filepath.Base(remotePath)
	dir := filepath.Dir(remotePath)

	if err := sess.Start(fmt.Sprintf("scp -qt %s", dir)); err != nil {
		return err
	}

	header := fmt.Sprintf("C%04o %d %s\n", mode, len(data), name)
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "\x00"); err != nil {
		return err
	}
	w.Close()

	return sess.Wait()
}

func (rs *realSSHSession) Close() error {
	return rs.client.Close()
}
