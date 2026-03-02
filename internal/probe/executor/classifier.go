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
	"curl", "wget", // read-only network access
	"fdisk -l", // list partitions (read-only)
	"mount",    // bare 'mount' lists mounts (handled specially)
	"kubeflow submit",
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
	"kubeflow cancel",
}

// CommandClassification is a deterministic classification result for a command line.
type CommandClassification struct {
	Level          protocol.CapabilityLevel `json:"level"`
	Category       string                   `json:"category"`
	SignatureKnown bool                     `json:"signature_known"`
	ReasonCode     string                   `json:"reason_code"`
}

// ClassifyCommand determines the minimum capability level required to run a command.
// It is conservative: unknown commands default to remediate.
func ClassifyCommand(command string, args []string) protocol.CapabilityLevel {
	return ClassifyCommandWithMetadata(command, args).Level
}

// ClassifyCommandWithMetadata classifies command capability and whether the mutation signature is known.
func ClassifyCommandWithMetadata(command string, args []string) CommandClassification {
	fullLower, baseLower := normalizedCommand(command, args)

	// Check remediate prefixes first (highest priority)
	for _, p := range remediatePrefixes {
		if strings.HasPrefix(fullLower, p) || strings.HasPrefix(baseLower, p) {
			return CommandClassification{
				Level:          protocol.CapRemediate,
				Category:       "mutation",
				SignatureKnown: true,
				ReasonCode:     "classifier.remediate_prefix",
			}
		}
	}

	// Check observe prefixes
	for _, p := range observePrefixes {
		if strings.HasPrefix(fullLower, p) {
			return CommandClassification{
				Level:          protocol.CapObserve,
				Category:       "observe",
				SignatureKnown: true,
				ReasonCode:     "classifier.observe_prefix",
			}
		}
	}

	// Check observe base commands
	for _, cmd := range observeCommands {
		if baseLower == cmd {
			// Special case: find with -exec or -delete is remediate
			if baseLower == "find" && (strings.Contains(fullLower, "-exec") || strings.Contains(fullLower, "-delete")) {
				return CommandClassification{
					Level:          protocol.CapRemediate,
					Category:       "mutation",
					SignatureKnown: true,
					ReasonCode:     "classifier.find_mutation_flags",
				}
			}
			return CommandClassification{
				Level:          protocol.CapObserve,
				Category:       "observe",
				SignatureKnown: true,
				ReasonCode:     "classifier.observe_command",
			}
		}
	}

	// Check diagnose prefixes
	for _, p := range diagnosePrefixes {
		if strings.HasPrefix(fullLower, p) {
			// wget with -O/-o (output file) is remediate
			if strings.HasPrefix(baseLower, "wget") && (strings.Contains(fullLower, " -o") || strings.Contains(fullLower, " -o ")) {
				return CommandClassification{
					Level:          protocol.CapRemediate,
					Category:       "mutation",
					SignatureKnown: true,
					ReasonCode:     "classifier.wget_output_file",
				}
			}
			return CommandClassification{
				Level:          protocol.CapDiagnose,
				Category:       "diagnose",
				SignatureKnown: true,
				ReasonCode:     "classifier.diagnose_prefix",
			}
		}
	}

	// Check diagnose base commands
	for _, cmd := range diagnoseCommands {
		if baseLower == cmd {
			return CommandClassification{
				Level:          protocol.CapDiagnose,
				Category:       "diagnose",
				SignatureKnown: true,
				ReasonCode:     "classifier.diagnose_command",
			}
		}
	}

	// Unknown commands default to remediate (conservative).
	return CommandClassification{
		Level:          protocol.CapRemediate,
		Category:       "mutation",
		SignatureKnown: false,
		ReasonCode:     "classifier.unknown_mutation_signature",
	}
}

func normalizedCommand(command string, args []string) (string, string) {
	trimmedCommand := strings.TrimSpace(command)
	fullCmd := trimmedCommand
	if len(args) > 0 {
		fullCmd = trimmedCommand + " " + strings.Join(args, " ")
	}
	fullLower := strings.ToLower(strings.TrimSpace(fullCmd))

	baseCommand := trimmedCommand
	if idx := strings.IndexAny(baseCommand, " \t"); idx > 0 {
		baseCommand = baseCommand[:idx]
	}
	baseLower := strings.ToLower(strings.TrimSpace(baseCommand))
	return fullLower, baseLower
}
