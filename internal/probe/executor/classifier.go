package executor

import (
	"strings"

	"github.com/marcus-qen/legator/internal/protocol"
)

// observeCommands are read-only, zero-risk commands.
var observeCommands = []string{
	"cat", "ls", "head", "tail", "df", "du", "free", "uptime", "uname",
	"hostname", "whoami", "id", "ps", "top", "netstat", "ss",
	"lsof", "file", "stat", "wc", "grep", "find",
	"journalctl", "which", "type", "echo", "date", "env", "printenv",
	"lsb_release", "arch", "nproc", "getent", "groups", "last", "w", "sleep", "true", "false",
}

// observePrefixes are command prefixes that are observe-level.
var observePrefixes = []string{
	"ip addr", "ip route", "ip link", "ip neigh",
	"systemctl status", "systemctl is-active", "systemctl is-enabled",
	"systemctl list-units", "systemctl list-timers",
	"docker ps", "docker images", "docker inspect",
	"podman ps", "podman images", "podman inspect",
}

// diagnoseCommands are analysis/debug commands (read + network probing).
var diagnoseCommands = []string{
	"strace", "ltrace", "tcpdump", "dig", "nslookup", "traceroute",
	"tracepath", "ping", "openssl", "nc", "ncat", "nmap",
	"iotop", "vmstat", "iostat", "sar", "dmesg", "lsblk",
	"blkid", "ss", "perf", "iftop", "nethogs",
}

// diagnosePrefixes are command prefixes that are diagnose-level.
var diagnosePrefixes = []string{
	"curl", "wget",   // read-only network access
	"fdisk -l",       // list partitions (read-only)
	"mount",          // bare 'mount' lists mounts (handled specially)
}

// remediatePrefixes are command prefixes always classified as remediate.
var remediatePrefixes = []string{
	"rm ", "rm\t", "mv ", "mv\t", "cp ", "cp\t",
	"chmod ", "chown ", "chgrp ",
	"mkdir ", "touch ", "tee ",
	"apt ", "apt-get ", "dpkg ", "yum ", "dnf ", "rpm ",
	"pip ", "pip3 ", "npm ", "gem ",
	"systemctl start", "systemctl stop", "systemctl restart",
	"systemctl enable", "systemctl disable", "systemctl mask",
	"service ",
	"reboot", "shutdown", "poweroff", "halt", "init ",
	"kill ", "pkill ", "killall ",
	"iptables ", "ip6tables ", "ufw ", "firewall-cmd ",
	"sed -i", "dd ", "mkfs", "mount ",
	"useradd", "userdel", "usermod", "groupadd", "groupdel",
	"passwd ", "chpasswd",
	"crontab ",
}

// ClassifyCommand determines the minimum capability level required to run a command.
// It is conservative: unknown commands default to remediate.
func ClassifyCommand(command string, args []string) protocol.CapabilityLevel {
	fullCmd := command
	if len(args) > 0 {
		fullCmd = command + " " + strings.Join(args, " ")
	}
	fullLower := strings.ToLower(strings.TrimSpace(fullCmd))
	baseLower := strings.ToLower(strings.TrimSpace(command))

	// Check remediate prefixes first (highest priority)
	for _, p := range remediatePrefixes {
		if strings.HasPrefix(fullLower, p) || strings.HasPrefix(baseLower, p) {
			return protocol.CapRemediate
		}
	}

	// Check observe prefixes
	for _, p := range observePrefixes {
		if strings.HasPrefix(fullLower, p) {
			return protocol.CapObserve
		}
	}

	// Check observe base commands
	for _, cmd := range observeCommands {
		if baseLower == cmd {
			// Special case: find with -exec or -delete is remediate
			if baseLower == "find" && (strings.Contains(fullLower, "-exec") || strings.Contains(fullLower, "-delete")) {
				return protocol.CapRemediate
			}
			return protocol.CapObserve
		}
	}

	// Check diagnose prefixes
	for _, p := range diagnosePrefixes {
		if strings.HasPrefix(fullLower, p) {
			// wget with -O (output file) is remediate
			if strings.HasPrefix(baseLower, "wget") && strings.Contains(fullLower, " -o") {
				return protocol.CapRemediate
			}
			return protocol.CapDiagnose
		}
	}

	// Check diagnose base commands
	for _, cmd := range diagnoseCommands {
		if baseLower == cmd {
			return protocol.CapDiagnose
		}
	}

	// Unknown commands default to remediate (conservative)
	return protocol.CapRemediate
}
