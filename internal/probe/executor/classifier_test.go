package executor

import (
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
)

func TestClassifyCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		args    []string
		want    protocol.CapabilityLevel
	}{
		// Observe
		{"cat", "cat", []string{"/etc/hostname"}, protocol.CapObserve},
		{"ls", "ls", []string{"-la", "/tmp"}, protocol.CapObserve},
		{"ps aux", "ps", []string{"aux"}, protocol.CapObserve},
		{"df -h", "df", []string{"-h"}, protocol.CapObserve},
		{"uptime", "uptime", nil, protocol.CapObserve},
		{"hostname", "hostname", nil, protocol.CapObserve},
		{"journalctl", "journalctl", []string{"-u", "nginx"}, protocol.CapObserve},
		{"systemctl status", "systemctl", []string{"status", "nginx"}, protocol.CapObserve},
		{"ip addr", "ip", []string{"addr", "show"}, protocol.CapObserve},
		{"echo", "echo", []string{"hello"}, protocol.CapObserve},
		{"find read-only", "find", []string{"/var", "-name", "*.log"}, protocol.CapObserve},
		{"free", "free", []string{"-m"}, protocol.CapObserve},

		// Diagnose
		{"ping", "ping", []string{"-c", "3", "8.8.8.8"}, protocol.CapDiagnose},
		{"dig", "dig", []string{"example.com"}, protocol.CapDiagnose},
		{"curl", "curl", []string{"-s", "http://example.com"}, protocol.CapDiagnose},
		{"tcpdump", "tcpdump", []string{"-i", "eth0"}, protocol.CapDiagnose},
		{"strace", "strace", []string{"-p", "1234"}, protocol.CapDiagnose},
		{"nmap", "nmap", []string{"-sP", "192.168.1.0/24"}, protocol.CapDiagnose},
		{"dmesg", "dmesg", nil, protocol.CapDiagnose},

		// Remediate
		{"rm", "rm", []string{"-rf", "/tmp/test"}, protocol.CapRemediate},
		{"systemctl restart", "systemctl", []string{"restart", "nginx"}, protocol.CapRemediate},
		{"apt install", "apt", []string{"install", "nginx"}, protocol.CapRemediate},
		{"kill", "kill", []string{"-9", "1234"}, protocol.CapRemediate},
		{"chmod", "chmod", []string{"755", "/tmp/test"}, protocol.CapRemediate},
		{"touch", "touch", []string{"/tmp/test"}, protocol.CapRemediate},
		{"reboot", "reboot", nil, protocol.CapRemediate},
		{"find -delete", "find", []string{"/tmp", "-name", "*.tmp", "-delete"}, protocol.CapRemediate},
		{"find -exec", "find", []string{"/tmp", "-exec", "rm", "{}", ";"}, protocol.CapRemediate},
		{"unknown command", "my-custom-script", []string{"--flag"}, protocol.CapRemediate},
		{"sed -i", "sed", []string{"-i", "s/old/new/", "/etc/config"}, protocol.CapRemediate},
		{"useradd", "useradd", []string{"newuser"}, protocol.CapRemediate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyCommand(tt.command, tt.args)
			if got != tt.want {
				t.Errorf("ClassifyCommand(%q, %v) = %s, want %s", tt.command, tt.args, got, tt.want)
			}
		})
	}
}
