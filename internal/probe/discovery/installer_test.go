package discovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"golang.org/x/crypto/ssh"
)

// mockSSHSession implements SSHSession for testing.
type mockSSHSession struct {
	commands  []string
	uploads   []uploadRecord
	runErr    error
	uploadErr error
}

type uploadRecord struct {
	data       []byte
	remotePath string
	mode       os.FileMode
}

func (m *mockSSHSession) RunCommand(cmd string) (string, error) {
	m.commands = append(m.commands, cmd)
	if m.runErr != nil {
		return "", m.runErr
	}
	return "", nil
}

func (m *mockSSHSession) UploadFile(data []byte, remotePath string, mode os.FileMode) error {
	m.uploads = append(m.uploads, uploadRecord{data, remotePath, mode})
	if m.uploadErr != nil {
		return m.uploadErr
	}
	return nil
}

func (m *mockSSHSession) Close() error { return nil }

// mockSSHDialer returns pre-configured sessions.
type mockSSHDialer struct {
	session *mockSSHSession
	dialErr error
}

func (m *mockSSHDialer) Dial(_ context.Context, _ string, _ *ssh.ClientConfig) (SSHSession, error) {
	if m.dialErr != nil {
		return nil, m.dialErr
	}
	return m.session, nil
}

// binaryFile creates a temp file with some bytes for testing.
func binaryFile(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "probe-bin")
	if err != nil {
		t.Fatalf("create temp binary: %v", err)
	}
	_, _ = f.Write([]byte("fake-binary-data"))
	f.Close()
	return f.Name()
}

func TestInstallerInstallWithBinary(t *testing.T) {
	sess := &mockSSHSession{}
	dialer := &mockSSHDialer{session: sess}

	installer := &Installer{
		Dialer: dialer,
	}

	binPath := binaryFile(t)
	target := InstallTarget{
		CandidateID:  "cand-123",
		IP:           "10.0.0.5",
		Port:         22,
		SSHUser:      "root",
		SSHKey:       testEd25519Key,
		BinaryPath:   binPath,
		ServerURL:    "https://cp.example.com",
		InstallToken: "tok-abc123",
	}

	result, err := installer.Install(context.Background(), target)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.CandidateID != "cand-123" {
		t.Fatalf("expected candidate id cand-123, got %q", result.CandidateID)
	}

	// Should have run mkdir, then systemctl daemon-reload, enable, start.
	if len(sess.commands) < 4 {
		t.Fatalf("expected >=4 commands, got %d: %v", len(sess.commands), sess.commands)
	}
	foundMkdir := false
	for _, cmd := range sess.commands {
		if fmt.Sprintf("%s", cmd) == fmt.Sprintf("mkdir -p %s", remoteInstallDir) {
			foundMkdir = true
			break
		}
	}
	if !foundMkdir {
		t.Errorf("expected mkdir command in %v", sess.commands)
	}

	// Should have uploaded the binary and the unit file.
	if len(sess.uploads) < 2 {
		t.Fatalf("expected >=2 uploads, got %d", len(sess.uploads))
	}
}

func TestInstallerInstallViaCurl(t *testing.T) {
	sess := &mockSSHSession{}
	dialer := &mockSSHDialer{session: sess}

	installer := &Installer{Dialer: dialer}

	target := InstallTarget{
		CandidateID:  "cand-456",
		IP:           "10.0.0.6",
		Port:         22,
		SSHUser:      "root",
		SSHKey:       testEd25519Key,
		BinaryPath:   "", // no local binary -> use curl install script
		ServerURL:    "https://cp.example.com",
		InstallToken: "tok-xyz789",
	}

	result, err := installer.Install(context.Background(), target)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success: %s", result.Error)
	}

	foundCurl := false
	for _, cmd := range sess.commands {
		if len(cmd) > 4 && cmd[:4] == "curl" {
			foundCurl = true
			break
		}
	}
	if !foundCurl {
		t.Errorf("expected curl command, got %v", sess.commands)
	}
}

func TestInstallerDialError(t *testing.T) {
	dialer := &mockSSHDialer{dialErr: errors.New("refused")}
	installer := &Installer{Dialer: dialer}

	target := InstallTarget{
		CandidateID: "cand-789",
		IP:          "10.0.0.7",
		Port:        22,
		SSHUser:     "root",
		SSHKey:      testEd25519Key,
	}

	result, err := installer.Install(context.Background(), target)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure on dial error")
	}
	if result.Error == "" {
		t.Fatal("expected error message")
	}
}

func TestInstallerRunCommandError(t *testing.T) {
	sess := &mockSSHSession{runErr: errors.New("permission denied")}
	dialer := &mockSSHDialer{session: sess}
	installer := &Installer{Dialer: dialer}

	binPath := binaryFile(t)
	target := InstallTarget{
		CandidateID:  "cand-err",
		IP:           "10.0.0.8",
		Port:         22,
		SSHUser:      "root",
		SSHKey:       testEd25519Key,
		BinaryPath:   binPath,
		ServerURL:    "https://cp.example.com",
		InstallToken: "tok",
	}

	result, err := installer.Install(context.Background(), target)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure on command error")
	}
}

func TestInstallerBadSSHKey(t *testing.T) {
	installer := &Installer{}
	target := InstallTarget{
		CandidateID: "cand-bad",
		IP:          "10.0.0.9",
		Port:        22,
		SSHKey:      "not-a-valid-pem-key",
	}

	result, err := installer.Install(context.Background(), target)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for bad SSH key")
	}
	if result.Error == "" {
		t.Fatal("expected error message for bad SSH key")
	}
}

func TestInstallerNoBinaryNoServer(t *testing.T) {
	sess := &mockSSHSession{}
	dialer := &mockSSHDialer{session: sess}
	installer := &Installer{Dialer: dialer}

	target := InstallTarget{
		CandidateID:  "cand-nobin",
		IP:           "10.0.0.10",
		Port:         22,
		SSHUser:      "root",
		SSHKey:       testEd25519Key,
		BinaryPath:   "",
		ServerURL:    "", // no server URL
		InstallToken: "",
	}

	result, err := installer.Install(context.Background(), target)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure when neither binary nor server URL provided")
	}
}

func TestBuildSystemdUnit(t *testing.T) {
	unit := buildSystemdUnit("/usr/local/bin/probe", "https://cp.example.com", "mytoken")
	if unit == "" {
		t.Fatal("expected non-empty unit content")
	}
	if !containsStr(unit, "[Unit]") {
		t.Error("missing [Unit] section")
	}
	if !containsStr(unit, "[Service]") {
		t.Error("missing [Service] section")
	}
	if !containsStr(unit, "[Install]") {
		t.Error("missing [Install] section")
	}
	if !containsStr(unit, "/usr/local/bin/probe") {
		t.Error("expected binary path in unit")
	}
	if !containsStr(unit, "https://cp.example.com") {
		t.Error("expected server URL in unit")
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// testEd25519Key is a throwaway Ed25519 private key for testing only.
// DO NOT use outside tests.
const testEd25519Key = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACAiq3fovdg1D9yKPu+mtBXkq5FL7nEyAF3HhvhY0t7dtAAAAJCtuUBprblA
aQAAAAtzc2gtZWQyNTUxOQAAACAiq3fovdg1D9yKPu+mtBXkq5FL7nEyAF3HhvhY0t7dtA
AAAECU7tXsST7BooC8V2BdhlvPMs0EJ6y369kAxExsI65pIyKrd+i92DUP3Io+76a0FeSr
kUvucTIAXceG+FjS3t20AAAADW1hcmN1c0BjYXN0cmE=
-----END OPENSSH PRIVATE KEY-----`
