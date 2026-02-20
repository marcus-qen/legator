/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

import (
	"testing"
)

func TestSSHToolName(t *testing.T) {
	tool := NewSSHTool(nil)
	if tool.Name() != "ssh.exec" {
		t.Errorf("Name() = %q, want 'ssh.exec'", tool.Name())
	}
}

func TestSSHToolCapability(t *testing.T) {
	tool := NewSSHTool(nil)
	cap := tool.Capability()

	if cap.Domain != "ssh" {
		t.Errorf("Domain = %q, want 'ssh'", cap.Domain)
	}
	if !cap.RequiresCredentials {
		t.Error("SSH should require credentials")
	}
	if !cap.RequiresConnection {
		t.Error("SSH should require connection")
	}
}

func TestSSHClassifyReadCommands(t *testing.T) {
	tool := NewSSHTool(map[string]*SSHCredential{
		"server1": {Host: "server1:22", User: "admin", Password: "test"},
	})

	readCmds := []string{
		"ls -la /var/log",
		"cat /etc/nginx/nginx.conf",
		"ps aux",
		"df -h",
		"uptime",
		"whoami",
		"uname -a",
		"systemctl status nginx",
		"journalctl -u sshd --tail 50",
		"grep error /var/log/syslog",
		"find /etc -name '*.conf'",
		"head -100 /var/log/auth.log",
		"free -m",
		"netstat -tlnp",
		"hostname",
	}

	for _, cmd := range readCmds {
		t.Run(cmd, func(t *testing.T) {
			args := map[string]interface{}{"host": "server1", "command": cmd}
			result := tool.ClassifyAction(args)
			if result.Tier != TierRead {
				t.Errorf("ClassifyAction(%q) tier = %v, want read", cmd, result.Tier)
			}
			if result.Blocked {
				t.Errorf("ClassifyAction(%q) blocked = true, want false", cmd)
			}
		})
	}
}

func TestSSHClassifyServiceMutations(t *testing.T) {
	tool := NewSSHTool(map[string]*SSHCredential{
		"server1": {Host: "server1:22", User: "admin", Password: "test", AllowSudo: true},
	})

	cmds := []string{
		"systemctl restart nginx",
		"systemctl stop apache2",
		"systemctl start postgresql",
		"kill -9 12345",
		"docker restart mycontainer",
	}

	for _, cmd := range cmds {
		t.Run(cmd, func(t *testing.T) {
			args := map[string]interface{}{"host": "server1", "command": cmd}
			result := tool.ClassifyAction(args)
			if result.Tier != TierServiceMutation {
				t.Errorf("ClassifyAction(%q) tier = %v, want service-mutation", cmd, result.Tier)
			}
		})
	}
}

func TestSSHClassifyDestructiveMutations(t *testing.T) {
	tool := NewSSHTool(map[string]*SSHCredential{
		"server1": {Host: "server1:22", User: "admin", Password: "test", AllowSudo: true},
	})

	cmds := []string{
		"rm /tmp/old-file.log",
		"chmod 755 /opt/app/start.sh",
		"chown www-data:www-data /var/www/html",
		"mv /etc/nginx/sites-available/old.conf /tmp/",
		"apt-get install nginx",
		"iptables -A INPUT -p tcp --dport 80 -j ACCEPT",
	}

	for _, cmd := range cmds {
		t.Run(cmd, func(t *testing.T) {
			args := map[string]interface{}{"host": "server1", "command": cmd}
			result := tool.ClassifyAction(args)
			if result.Tier != TierDestructiveMutation {
				t.Errorf("ClassifyAction(%q) tier = %v, want destructive-mutation", cmd, result.Tier)
			}
		})
	}
}

func TestSSHBlockedCommands(t *testing.T) {
	tool := NewSSHTool(map[string]*SSHCredential{
		"server1": {Host: "server1:22", User: "admin", Password: "test", AllowSudo: true},
	})

	blockedCmds := []string{
		"dd if=/dev/zero of=/dev/sda bs=1M",
		"mkfs.ext4 /dev/sdb1",
		"fdisk /dev/sda",
		"psql -c 'DROP TABLE users'",
		"mysql -e 'DELETE FROM logs'",
		"mongo --eval 'db.users.drop()'",
		"redis-cli FLUSHALL",
	}

	for _, cmd := range blockedCmds {
		t.Run(cmd, func(t *testing.T) {
			args := map[string]interface{}{"host": "server1", "command": cmd}
			result := tool.ClassifyAction(args)
			if !result.Blocked {
				t.Errorf("ClassifyAction(%q) should be blocked", cmd)
			}
		})
	}
}

func TestSSHProtectedPaths(t *testing.T) {
	tool := NewSSHTool(map[string]*SSHCredential{
		"server1": {Host: "server1:22", User: "admin", Password: "test"},
	})

	tests := []struct {
		cmd     string
		blocked bool
	}{
		{"cat /etc/shadow", true},          // Always blocked (even read)
		{"rm /etc/shadow", true},           // Write + protected
		{"cat /var/log/syslog", false},     // Not protected
		{"rm /boot/vmlinuz", true},         // Protected path
		{"ls /boot/", false},               // Read of /boot is allowed (ls doesn't trigger write check)
		{"cat /etc/passwd", false},         // passwd is readable (shadow is not)
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			args := map[string]interface{}{"host": "server1", "command": tt.cmd}
			result := tool.ClassifyAction(args)
			if result.Blocked != tt.blocked {
				t.Errorf("ClassifyAction(%q) blocked = %v, want %v (reason: %s)",
					tt.cmd, result.Blocked, tt.blocked, result.BlockReason)
			}
		})
	}
}

func TestSSHSudoBlocking(t *testing.T) {
	// Without AllowSudo
	toolNoSudo := NewSSHTool(map[string]*SSHCredential{
		"server1": {Host: "server1:22", User: "admin", Password: "test", AllowSudo: false},
	})

	args := map[string]interface{}{"host": "server1", "command": "sudo systemctl restart nginx"}
	result := toolNoSudo.ClassifyAction(args)
	if !result.Blocked {
		t.Error("sudo should be blocked when AllowSudo=false")
	}

	// With AllowSudo
	toolSudo := NewSSHTool(map[string]*SSHCredential{
		"server1": {Host: "server1:22", User: "admin", Password: "test", AllowSudo: true},
	})

	result = toolSudo.ClassifyAction(args)
	if result.Blocked {
		t.Error("sudo should be allowed when AllowSudo=true")
	}
}

func TestSSHRootBlocking(t *testing.T) {
	// Root without AllowRoot
	tool := NewSSHTool(map[string]*SSHCredential{
		"server1": {Host: "server1:22", User: "root", Password: "test", AllowRoot: false},
	})

	_, err := tool.getConnection("server1")
	if err == nil {
		t.Error("Root login should be rejected when AllowRoot=false")
	}

	// Root with AllowRoot (will fail to connect, but shouldn't be rejected by policy)
	tool2 := NewSSHTool(map[string]*SSHCredential{
		"server1": {Host: "server1:22", User: "root", Password: "test", AllowRoot: true},
	})
	_, err = tool2.getConnection("server1")
	// Should fail with connection error, not policy error
	if err != nil && !isConnectionError(err) {
		t.Errorf("Expected connection error, got policy error: %v", err)
	}
}

func TestSSHNoCredential(t *testing.T) {
	tool := NewSSHTool(map[string]*SSHCredential{})

	_, err := tool.getConnection("unknown-host")
	if err == nil {
		t.Error("Should fail with no credential configured")
	}
	if err.Error() != `no SSH credential configured for host "unknown-host"` {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestSSHMissingArgs(t *testing.T) {
	tool := NewSSHTool(nil)

	// Missing host
	_, err := tool.Execute(nil, map[string]interface{}{"command": "ls"})
	if err == nil || err.Error() != "ssh: host is required" {
		t.Errorf("Expected host required error, got: %v", err)
	}

	// Missing command
	_, err = tool.Execute(nil, map[string]interface{}{"host": "server1"})
	if err == nil || err.Error() != "ssh: command is required" {
		t.Errorf("Expected command required error, got: %v", err)
	}
}

func TestClassifySSHCommand(t *testing.T) {
	tests := []struct {
		cmd  string
		want ActionTier
	}{
		{"ls -la", TierRead},
		{"cat /etc/hosts", TierRead},
		{"ps aux", TierRead},
		{"df -h", TierRead},
		{"uptime", TierRead},
		{"systemctl status nginx", TierRead},
		{"journalctl -u sshd", TierRead},
		{"grep error /var/log/syslog", TierRead},
		{"curl http://localhost:8080/health", TierRead},

		{"systemctl restart nginx", TierServiceMutation},
		{"systemctl stop apache2", TierServiceMutation},
		{"kill -9 1234", TierServiceMutation},
		{"docker restart web", TierServiceMutation},

		{"rm /tmp/file", TierDestructiveMutation},
		{"chmod 755 /opt/app", TierDestructiveMutation},
		{"chown root:root /etc/config", TierDestructiveMutation},
		{"apt-get install nginx", TierDestructiveMutation},
		{"iptables -A INPUT -j DROP", TierDestructiveMutation},
		{"useradd newuser", TierDestructiveMutation},
		{"sed -i 's/old/new/' /etc/config", TierDestructiveMutation},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := classifySSHCommand(tt.cmd)
			if got != tt.want {
				t.Errorf("classifySSHCommand(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

// isConnectionError returns true if the error is a network connection error (not a policy error).
func isConnectionError(err error) bool {
	msg := err.Error()
	return !contains(msg, "not permitted") && !contains(msg, "no SSH credential")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
