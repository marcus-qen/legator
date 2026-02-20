/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// maxSSHOutput is the maximum bytes returned from an SSH command.
const maxSSHOutput = 8192

// defaultSSHTimeout is the default per-command timeout.
const defaultSSHTimeout = 30 * time.Second

// SSHCredential holds authentication details for an SSH connection.
type SSHCredential struct {
	// Host is the SSH server address (host:port).
	Host string

	// User is the SSH username.
	User string

	// PrivateKey is a PEM-encoded private key (mutually exclusive with Password).
	PrivateKey []byte

	// Password is the SSH password (mutually exclusive with PrivateKey).
	Password string

	// AllowSudo permits sudo commands on this host.
	AllowSudo bool

	// AllowRoot permits root login on this host.
	AllowRoot bool
}

// SSHTool executes commands on remote servers via SSH.
type SSHTool struct {
	// credentials maps host identifiers to SSH credentials.
	credentials map[string]*SSHCredential

	// protectedPaths are paths that cannot be written to or deleted.
	protectedPaths []string

	// blockedCommands are commands that are always rejected.
	blockedCommands []string

	// commandTimeout is the max duration per SSH command.
	commandTimeout time.Duration

	// connections caches SSH client connections (reused within a run).
	connections map[string]*ssh.Client
}

// NewSSHTool creates a new SSH tool.
func NewSSHTool(creds map[string]*SSHCredential) *SSHTool {
	return &SSHTool{
		credentials: creds,
		protectedPaths: []string{
			"/etc/shadow", "/etc/gshadow",
			"/boot/", "/dev/",
			"~/.ssh/id_*", "~/.ssh/authorized_keys",
			"/root/.ssh/",
		},
		blockedCommands: []string{
			"dd", "mkfs", "fdisk", "parted", "wipefs",
			"psql", "mysql", "mongo", "mongosh", "redis-cli",
			"shred", "srm",
		},
		commandTimeout: defaultSSHTimeout,
		connections:    make(map[string]*ssh.Client),
	}
}

// Name implements Tool.
func (t *SSHTool) Name() string { return "ssh.exec" }

// Description implements Tool.
func (t *SSHTool) Description() string {
	return "Execute a command on a remote server via SSH. Returns stdout and stderr. " +
		"Parameters: host (string, required), command (string, required), timeout (string, optional, e.g. '30s')."
}

// Parameters implements Tool.
func (t *SSHTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"host": map[string]interface{}{
				"type":        "string",
				"description": "Target host identifier (must match an environment SSH credential).",
			},
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Shell command to execute on the remote server.",
			},
			"timeout": map[string]interface{}{
				"type":        "string",
				"description": "Command timeout (e.g. '30s', '2m'). Default: 30s.",
			},
		},
		"required": []string{"host", "command"},
	}
}

// Capability implements ClassifiableTool.
func (t *SSHTool) Capability() ToolCapability {
	return ToolCapability{
		Domain:              "ssh",
		SupportedTiers:      []ActionTier{TierRead, TierServiceMutation, TierDestructiveMutation},
		RequiresCredentials: true,
		RequiresConnection:  true,
	}
}

// ClassifyAction implements ClassifiableTool.
func (t *SSHTool) ClassifyAction(args map[string]interface{}) ActionClassification {
	cmd, _ := args["command"].(string)
	host, _ := args["host"].(string)

	if cmd == "" {
		return ActionClassification{
			Tier:        TierRead,
			Target:      host,
			Description: "empty command",
		}
	}

	// Check blocked commands first
	if reason := t.isBlockedCommand(cmd); reason != "" {
		return ActionClassification{
			Tier:        TierDataMutation,
			Target:      fmt.Sprintf("%s: %s", host, cmd),
			Description: reason,
			Blocked:     true,
			BlockReason: reason,
		}
	}

	// Check protected paths
	if reason := t.touchesProtectedPath(cmd); reason != "" {
		return ActionClassification{
			Tier:        TierDestructiveMutation,
			Target:      fmt.Sprintf("%s: %s", host, cmd),
			Description: reason,
			Blocked:     true,
			BlockReason: reason,
		}
	}

	// Check sudo
	if strings.Contains(cmd, "sudo") {
		cred := t.credentials[host]
		if cred == nil || !cred.AllowSudo {
			return ActionClassification{
				Tier:        TierDestructiveMutation,
				Target:      fmt.Sprintf("%s: %s", host, cmd),
				Description: "sudo command",
				Blocked:     true,
				BlockReason: "sudo not permitted for this host",
			}
		}
	}

	// Classify by command
	tier := classifySSHCommand(cmd)
	return ActionClassification{
		Tier:        tier,
		Target:      fmt.Sprintf("%s: %s", host, cmd),
		Description: fmt.Sprintf("SSH %s on %s", tier, host),
	}
}

// Execute implements Tool.
func (t *SSHTool) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	host, _ := args["host"].(string)
	cmd, _ := args["command"].(string)

	if host == "" {
		return "", fmt.Errorf("ssh: host is required")
	}
	if cmd == "" {
		return "", fmt.Errorf("ssh: command is required")
	}

	// Pre-flight: classify and check
	classification := t.ClassifyAction(args)
	if classification.Blocked {
		return "", fmt.Errorf("ssh: action blocked — %s", classification.BlockReason)
	}

	// Get or establish connection
	client, err := t.getConnection(host)
	if err != nil {
		return "", fmt.Errorf("ssh: connection failed to %s — %v", host, err)
	}

	// Parse timeout
	timeout := t.commandTimeout
	if ts, ok := args["timeout"].(string); ok && ts != "" {
		if d, err := time.ParseDuration(ts); err == nil {
			timeout = d
		}
	}

	// Create session with timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	session, err := client.NewSession()
	if err != nil {
		// Connection may be stale — try reconnecting
		delete(t.connections, host)
		client, err = t.getConnection(host)
		if err != nil {
			return "", fmt.Errorf("ssh: reconnection failed to %s — %v", host, err)
		}
		session, err = client.NewSession()
		if err != nil {
			return "", fmt.Errorf("ssh: session creation failed — %v", err)
		}
	}
	defer session.Close()

	// Capture stdout + stderr
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Run with context cancellation
	done := make(chan error, 1)
	go func() {
		done <- session.Run(cmd)
	}()

	select {
	case err := <-done:
		output := stdout.String()
		if stderr.Len() > 0 {
			output += "\n--- stderr ---\n" + stderr.String()
		}

		// Truncate output
		if len(output) > maxSSHOutput {
			output = output[:maxSSHOutput] + "\n... [truncated at 8KB]"
		}

		if err != nil {
			// Include output even on error (non-zero exit code)
			if output == "" {
				return fmt.Sprintf("Command failed: %v", err), nil
			}
			return output + fmt.Sprintf("\n--- exit error: %v ---", err), nil
		}
		return output, nil

	case <-ctx.Done():
		session.Signal(ssh.SIGTERM)
		return "", fmt.Errorf("ssh: command timed out after %v", timeout)
	}
}

// getConnection returns a cached or new SSH connection to the host.
func (t *SSHTool) getConnection(host string) (*ssh.Client, error) {
	if client, ok := t.connections[host]; ok {
		return client, nil
	}

	cred, ok := t.credentials[host]
	if !ok {
		return nil, fmt.Errorf("no SSH credential configured for host %q", host)
	}

	// Check root login
	if cred.User == "root" && !cred.AllowRoot {
		return nil, fmt.Errorf("root login not permitted for host %q", host)
	}

	// Build auth methods
	var authMethods []ssh.AuthMethod
	if len(cred.PrivateKey) > 0 {
		signer, err := ssh.ParsePrivateKey(cred.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse private key for %s: %v", host, err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if cred.Password != "" {
		authMethods = append(authMethods, ssh.Password(cred.Password))
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no authentication method configured for host %q", host)
	}

	config := &ssh.ClientConfig{
		User:            cred.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: host key verification in v0.3.0
		Timeout:         10 * time.Second,
	}

	// Ensure host has port
	addr := cred.Host
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = addr + ":22"
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, err
	}

	t.connections[host] = client
	return client, nil
}

// Close cleans up all cached SSH connections.
func (t *SSHTool) Close() {
	for host, client := range t.connections {
		client.Close()
		delete(t.connections, host)
	}
}

// isBlockedCommand checks if the command uses a blocked binary.
func (t *SSHTool) isBlockedCommand(cmd string) string {
	// Extract the base command (handle sudo, pipes, etc.)
	parts := strings.Fields(cmd)
	for _, part := range parts {
		// Skip shell operators
		if part == "|" || part == "&&" || part == "||" || part == ";" || part == ">" || part == ">>" || part == "<" {
			continue
		}
		if part == "sudo" || part == "env" || part == "nice" || part == "nohup" || part == "timeout" {
			continue
		}
		// Check against blocked list
		base := part
		if idx := strings.LastIndex(part, "/"); idx >= 0 {
			base = part[idx+1:]
		}
		for _, blocked := range t.blockedCommands {
			if strings.EqualFold(base, blocked) {
				return fmt.Sprintf("blocked command: %s (data-mutation risk)", blocked)
			}
			// Handle commands with subcommands like mkfs.ext4, mongosh.exe
			if dotIdx := strings.Index(base, "."); dotIdx > 0 {
				if strings.EqualFold(base[:dotIdx], blocked) {
					return fmt.Sprintf("blocked command: %s (data-mutation risk)", blocked)
				}
			}
		}
		// Only check the first actual command in the pipeline
		break
	}
	return ""
}

// touchesProtectedPath checks if the command targets a protected path.
func (t *SSHTool) touchesProtectedPath(cmd string) string {
	cmdLower := strings.ToLower(cmd)

	// Check for write/delete operations on protected paths
	writeOps := []string{"rm ", "mv ", "cp ", "chmod ", "chown ", "truncate ", "> ", ">> ", "tee "}
	isWrite := false
	for _, op := range writeOps {
		if strings.Contains(cmdLower, op) {
			isWrite = true
			break
		}
	}

	// Also check cat/read of explicitly blocked files (shadow)
	for _, path := range t.protectedPaths {
		pathLower := strings.ToLower(path)
		if strings.Contains(cmdLower, pathLower) {
			if isWrite || pathLower == "/etc/shadow" || pathLower == "/etc/gshadow" {
				return fmt.Sprintf("protected path: %s", path)
			}
		}
	}

	return ""
}

// classifySSHCommand determines the action tier of an SSH command.
func classifySSHCommand(cmd string) ActionTier {
	// Extract the base command
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return TierRead
	}

	base := parts[0]
	if base == "sudo" && len(parts) > 1 {
		base = parts[1]
	}

	// Strip path prefix
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}

	baseLower := strings.ToLower(base)

	// Read-only commands
	readCmds := map[string]bool{
		"ls": true, "cat": true, "head": true, "tail": true, "less": true, "more": true,
		"grep": true, "find": true, "wc": true, "sort": true, "uniq": true, "diff": true,
		"ps": true, "top": true, "htop": true, "df": true, "du": true, "free": true,
		"uptime": true, "whoami": true, "id": true, "hostname": true, "uname": true,
		"date": true, "which": true, "whereis": true, "file": true, "stat": true,
		"lsof": true, "netstat": true, "ss": true, "ip": true, "ifconfig": true,
		"mount": true, "lsblk": true, "blkid": true, "dmidecode": true,
		"rpm": true, "dpkg": true, "apt": true, "yum": true, "dnf": true, // read: list/query
		"systemctl": true, // classify further below
		"journalctl": true, "dmesg": true,
		"curl": true, "wget": true, // read: downloading info
		"echo": true, "printf": true, "test": true,
	}

	// systemctl: status/list/show = read, restart/start/stop = service-mutation
	if baseLower == "systemctl" {
		if len(parts) > 1 {
			subCmd := strings.ToLower(parts[len(parts)-2]) // second-to-last is often the action after sudo
			if base == "sudo" && len(parts) > 2 {
				subCmd = strings.ToLower(parts[2])
			} else {
				subCmd = strings.ToLower(parts[1])
			}
			switch subCmd {
			case "restart", "start", "stop", "reload", "enable", "disable":
				return TierServiceMutation
			}
		}
		return TierRead
	}

	if readCmds[baseLower] {
		return TierRead
	}

	// Service mutation commands
	serviceMutationCmds := map[string]bool{
		"service": true, "systemctl": true, // already handled above
		"kill":    true, "pkill": true, "killall": true,
		"reboot":  true, "shutdown": true, "halt": true, "poweroff": true,
		"docker":  true, "podman": true, "crictl": true,
	}

	if serviceMutationCmds[baseLower] {
		return TierServiceMutation
	}

	// Destructive mutation commands
	destructiveCmds := map[string]bool{
		"rm": true, "rmdir": true, "mv": true,
		"chmod": true, "chown": true, "chgrp": true,
		"cp": true, "rsync": true, "scp": true,
		"tar": true, "gzip": true, "bzip2": true, "xz": true, "zip": true, "unzip": true,
		"sed": true, "awk": true, "perl": true, "python": true, "python3": true,
		"tee": true, "truncate": true,
		"useradd": true, "userdel": true, "usermod": true,
		"groupadd": true, "groupdel": true, "groupmod": true,
		"iptables": true, "ufw": true, "firewall-cmd": true,
		"apt-get": true, "yum": true, "dnf": true, // install/remove = destructive
		"pip": true, "npm": true, "gem": true,
		"make": true, "cmake": true,
	}

	if destructiveCmds[baseLower] {
		return TierDestructiveMutation
	}

	// Default: treat unknown commands as service mutations (conservative but not blocking)
	return TierServiceMutation
}
