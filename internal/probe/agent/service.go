package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	serviceName = "probe-agent"
	unitPath    = "/etc/systemd/system/probe-agent.service"
)

// unitTemplate generates the systemd unit file content.
func unitTemplate(probeBin, configDir string) string {
	return fmt.Sprintf(`[Unit]
Description=Legator Probe Agent
Documentation=https://legator.io
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s run --config-dir %s
Restart=always
RestartSec=5
User=root
LimitNOFILE=65536

# Security hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
ReadWritePaths=/var/lib/legator /var/log/legator
PrivateTmp=yes

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=probe-agent

[Install]
WantedBy=multi-user.target
`, probeBin, configDir)
}

// ServiceInstall creates and enables the systemd service.
func ServiceInstall(configDir string) error {
	// Find the probe binary
	probeBin, err := os.Executable()
	if err != nil {
		probeBin = "/usr/local/bin/probe"
	}
	probeBin, _ = filepath.Abs(probeBin)

	if configDir == "" {
		configDir = DefaultConfigDir
	}

	// Verify config exists
	if _, err := os.Stat(ConfigPath(configDir)); os.IsNotExist(err) {
		return fmt.Errorf("config not found at %s — run 'probe init' first", ConfigPath(configDir))
	}

	// Write unit file
	unit := unitTemplate(probeBin, configDir)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	// Reload systemd
	if err := systemctl("daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}

	// Enable and start
	if err := systemctl("enable", serviceName); err != nil {
		return fmt.Errorf("enable: %w", err)
	}

	if err := systemctl("start", serviceName); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	fmt.Printf("✅ Service %s installed and started\n", serviceName)
	fmt.Printf("   Unit: %s\n", unitPath)
	fmt.Printf("   Status: systemctl status %s\n", serviceName)
	fmt.Printf("   Logs: journalctl -u %s -f\n", serviceName)
	return nil
}

// ServiceRemove stops and removes the systemd service.
func ServiceRemove() error {
	_ = systemctl("stop", serviceName)
	_ = systemctl("disable", serviceName)

	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file: %w", err)
	}

	_ = systemctl("daemon-reload")

	fmt.Printf("✅ Service %s removed\n", serviceName)
	return nil
}

// ServiceStatus shows the systemd service status.
func ServiceStatus() error {
	cmd := exec.Command("systemctl", "status", serviceName, "--no-pager")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		// systemctl status returns non-zero for inactive services
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 3 {
			return nil // inactive is valid, not an error
		}
	}
	return err
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return nil
}
