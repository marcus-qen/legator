//go:build integration
// +build integration

/*
Copyright 2026.

SSH integration test — requires a real SSH server.
Run with: go test ./internal/tools/ -tags=integration -v
Set SSH_TEST_HOST, SSH_TEST_USER, SSH_TEST_KEY_PATH environment variables.
*/

package tools

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestSSHIntegrationDirectConnect(t *testing.T) {
	host := os.Getenv("SSH_TEST_HOST")
	user := os.Getenv("SSH_TEST_USER")
	keyPath := os.Getenv("SSH_TEST_KEY_PATH")

	if host == "" || user == "" || keyPath == "" {
		t.Skip("SSH_TEST_HOST, SSH_TEST_USER, SSH_TEST_KEY_PATH not set")
	}

	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("Failed to read SSH key: %v", err)
	}

	tool := NewSSHTool(map[string]*SSHCredential{
		"test-server": {
			Host:       host,
			User:       user,
			PrivateKey: key,
			AllowSudo:  false,
			AllowRoot:  false,
		},
	})
	defer tool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test 1: Basic connectivity
	t.Run("whoami", func(t *testing.T) {
		output, err := tool.Execute(ctx, map[string]interface{}{
			"host":    "test-server",
			"command": "whoami",
		})
		if err != nil {
			t.Fatalf("whoami failed: %v", err)
		}
		t.Logf("whoami output: %s", output)
		if output == "" {
			t.Error("Expected non-empty output from whoami")
		}
	})

	// Test 2: System info
	t.Run("uname", func(t *testing.T) {
		output, err := tool.Execute(ctx, map[string]interface{}{
			"host":    "test-server",
			"command": "uname -a",
		})
		if err != nil {
			t.Fatalf("uname failed: %v", err)
		}
		t.Logf("uname output: %s", output)
	})

	// Test 3: Read a file
	t.Run("read-hosts", func(t *testing.T) {
		output, err := tool.Execute(ctx, map[string]interface{}{
			"host":    "test-server",
			"command": "cat /etc/hostname",
		})
		if err != nil {
			t.Fatalf("cat hostname failed: %v", err)
		}
		t.Logf("hostname: %s", output)
	})

	// Test 4: Disk usage
	t.Run("disk-usage", func(t *testing.T) {
		output, err := tool.Execute(ctx, map[string]interface{}{
			"host":    "test-server",
			"command": "df -h /",
		})
		if err != nil {
			t.Fatalf("df failed: %v", err)
		}
		t.Logf("disk usage:\n%s", output)
	})

	// Test 5: Blocked command should fail
	t.Run("blocked-dd", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]interface{}{
			"host":    "test-server",
			"command": "dd if=/dev/zero of=/tmp/test bs=1M count=1",
		})
		if err == nil {
			t.Error("dd should be blocked")
		}
		t.Logf("Correctly blocked: %v", err)
	})

	// Test 6: Shadow file should be blocked
	t.Run("blocked-shadow", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]interface{}{
			"host":    "test-server",
			"command": "cat /etc/shadow",
		})
		if err == nil {
			t.Error("cat /etc/shadow should be blocked")
		}
		t.Logf("Correctly blocked: %v", err)
	})

	// Test 7: sudo should be blocked (AllowSudo=false)
	t.Run("blocked-sudo", func(t *testing.T) {
		_, err := tool.Execute(ctx, map[string]interface{}{
			"host":    "test-server",
			"command": "sudo ls /root",
		})
		if err == nil {
			t.Error("sudo should be blocked")
		}
		t.Logf("Correctly blocked: %v", err)
	})
}
