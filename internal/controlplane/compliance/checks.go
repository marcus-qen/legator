package compliance

import (
	"context"
	"fmt"
	"strings"
)

// BuiltinChecks returns the set of built-in compliance checks.
func BuiltinChecks() []ComplianceCheck {
	return []ComplianceCheck{
		{
			ID:          "os-patching",
			Name:        "OS Security Patches",
			Category:    "patching",
			Description: "Check whether security updates are available on the system",
			Severity:    SeverityHigh,
			CheckFunc:   checkOSPatching,
		},
		{
			ID:          "ssh-password-auth",
			Name:        "SSH Password Authentication",
			Category:    "ssh",
			Description: "Ensure SSH password authentication is disabled (key-only access)",
			Severity:    SeverityHigh,
			CheckFunc:   checkSSHPasswordAuth,
		},
		{
			ID:          "ssh-root-login",
			Name:        "SSH Root Login",
			Category:    "ssh",
			Description: "Ensure SSH root login is disabled",
			Severity:    SeverityCritical,
			CheckFunc:   checkSSHRootLogin,
		},
		{
			ID:          "firewall-active",
			Name:        "Firewall Active",
			Category:    "firewall",
			Description: "Verify that iptables or nftables rules are present and active",
			Severity:    SeverityHigh,
			CheckFunc:   checkFirewallActive,
		},
		{
			ID:          "disk-encryption",
			Name:        "Disk Encryption (LUKS)",
			Category:    "storage",
			Description: "Check for LUKS-encrypted volumes on the system",
			Severity:    SeverityMedium,
			CheckFunc:   checkDiskEncryption,
		},
		{
			ID:          "passwordless-accounts",
			Name:        "Passwordless User Accounts",
			Category:    "accounts",
			Description: "Ensure no user accounts have empty passwords",
			Severity:    SeverityCritical,
			CheckFunc:   checkPasswordlessAccounts,
		},
		{
			ID:          "unnecessary-services",
			Name:        "Unnecessary Services",
			Category:    "services",
			Description: "Check that known-risky services (telnet, rsh, rlogin) are not running",
			Severity:    SeverityMedium,
			CheckFunc:   checkUnnecessaryServices,
		},
	}
}

// checkOSPatching checks for available security updates using apt, yum, or dnf.
func checkOSPatching(ctx context.Context, exec ProbeExecutor) (status, evidence string, err error) {
	// Try apt first (Debian/Ubuntu)
	out, code, execErr := exec(ctx, "apt-get -s -o Dpkg::Options::='--force-confdef' dist-upgrade 2>/dev/null | grep -c '^Inst' || true")
	if execErr == nil && code == 0 {
		count := strings.TrimSpace(out)
		if count == "" || count == "0" {
			return StatusPass, "No pending security updates (apt)", nil
		}
		return StatusFail, fmt.Sprintf("%s security update(s) available via apt", count), nil
	}

	// Try yum/dnf (RHEL/CentOS/Fedora)
	out, code, execErr = exec(ctx, "yum check-update --security -q 2>/dev/null | grep -c '\\.' || dnf check-update --security -q 2>/dev/null | grep -c '\\.' || true")
	if execErr == nil && code == 0 {
		count := strings.TrimSpace(out)
		if count == "" || count == "0" {
			return StatusPass, "No pending security updates (yum/dnf)", nil
		}
		return StatusFail, fmt.Sprintf("%s security update(s) available via yum/dnf", count), nil
	}

	return StatusUnknown, "Could not determine update status (no apt/yum/dnf found or access denied)", nil
}

// checkSSHPasswordAuth checks that PasswordAuthentication is set to no in sshd_config.
func checkSSHPasswordAuth(ctx context.Context, exec ProbeExecutor) (status, evidence string, err error) {
	out, _, execErr := exec(ctx, "sshd -T 2>/dev/null | grep -i '^passwordauthentication' | awk '{print $2}'")
	if execErr != nil {
		return StatusUnknown, "Could not query sshd configuration", nil
	}

	val := strings.ToLower(strings.TrimSpace(out))
	switch val {
	case "no":
		return StatusPass, "SSH PasswordAuthentication is disabled", nil
	case "yes":
		return StatusFail, "SSH PasswordAuthentication is enabled — key-only access recommended", nil
	default:
		// Try reading config file directly
		out2, _, _ := exec(ctx, "grep -i 'PasswordAuthentication' /etc/ssh/sshd_config 2>/dev/null | grep -v '^#' | tail -1")
		if strings.TrimSpace(out2) == "" {
			return StatusWarning, "PasswordAuthentication not explicitly set in sshd_config (defaults to yes on many distros)", nil
		}
		return StatusUnknown, fmt.Sprintf("Could not determine SSH password auth status (raw: %q)", strings.TrimSpace(out)), nil
	}
}

// checkSSHRootLogin checks that PermitRootLogin is set to no.
func checkSSHRootLogin(ctx context.Context, exec ProbeExecutor) (status, evidence string, err error) {
	out, _, execErr := exec(ctx, "sshd -T 2>/dev/null | grep -i '^permitrootlogin' | awk '{print $2}'")
	if execErr != nil {
		return StatusUnknown, "Could not query sshd configuration", nil
	}

	val := strings.ToLower(strings.TrimSpace(out))
	switch val {
	case "no":
		return StatusPass, "SSH root login is disabled", nil
	case "yes":
		return StatusFail, "SSH root login is permitted — disable PermitRootLogin in sshd_config", nil
	case "prohibit-password", "without-password":
		return StatusWarning, fmt.Sprintf("SSH root login is partially restricted (%s) — recommend setting to 'no'", val), nil
	default:
		return StatusUnknown, fmt.Sprintf("Could not determine PermitRootLogin value (raw: %q)", val), nil
	}
}

// checkFirewallActive checks that firewall rules are in place.
func checkFirewallActive(ctx context.Context, exec ProbeExecutor) (status, evidence string, err error) {
	// Check nftables first
	out, code, _ := exec(ctx, "nft list ruleset 2>/dev/null | wc -l")
	if code == 0 {
		if n := strings.TrimSpace(out); n != "" && n != "0" {
			return StatusPass, fmt.Sprintf("nftables active with rules (%s lines)", n), nil
		}
	}

	// Check iptables
	out, code, _ = exec(ctx, "iptables -L -n 2>/dev/null | grep -c 'Chain' || true")
	if code == 0 {
		count := strings.TrimSpace(out)
		if count != "" && count != "0" && count != "3" {
			// More than 3 chains (default policy chains) means custom rules
			return StatusPass, fmt.Sprintf("iptables active (%s chains)", count), nil
		}
		// Check if there are any non-policy rules
		out2, _, _ := exec(ctx, "iptables -L -n 2>/dev/null | grep -v '^Chain\\|^target\\|^$' | grep -c '.' || true")
		if ruleCount := strings.TrimSpace(out2); ruleCount != "" && ruleCount != "0" {
			return StatusPass, fmt.Sprintf("iptables active with %s rule(s)", ruleCount), nil
		}
		return StatusWarning, "iptables found but no custom rules detected (only default policy chains)", nil
	}

	// Check ufw
	out, code, _ = exec(ctx, "ufw status 2>/dev/null | head -1")
	if code == 0 && strings.Contains(strings.ToLower(out), "active") {
		return StatusPass, "UFW firewall is active", nil
	}

	return StatusFail, "No active firewall detected (nftables, iptables, and ufw all inactive or not found)", nil
}

// checkDiskEncryption checks for LUKS-encrypted block devices.
func checkDiskEncryption(ctx context.Context, exec ProbeExecutor) (status, evidence string, err error) {
	out, code, execErr := exec(ctx, "lsblk -o NAME,TYPE 2>/dev/null | grep -c 'crypt' || true")
	if execErr != nil {
		return StatusUnknown, "Could not run lsblk to check disk encryption", nil
	}

	if code == 0 {
		count := strings.TrimSpace(out)
		if count != "" && count != "0" {
			return StatusPass, fmt.Sprintf("%s LUKS-encrypted volume(s) detected", count), nil
		}
	}

	// Try dmsetup
	out2, _, _ := exec(ctx, "dmsetup ls --target crypt 2>/dev/null | grep -c '.' || true")
	if c := strings.TrimSpace(out2); c != "" && c != "0" {
		return StatusPass, fmt.Sprintf("%s dm-crypt device(s) detected", c), nil
	}

	return StatusWarning, "No LUKS/dm-crypt encrypted volumes detected — consider encrypting sensitive data volumes", nil
}

// checkPasswordlessAccounts checks /etc/shadow for accounts with empty passwords.
func checkPasswordlessAccounts(ctx context.Context, exec ProbeExecutor) (status, evidence string, err error) {
	out, code, execErr := exec(ctx, "awk -F: '($2 == \"\" || $2 == \"!\") && $1 != \"\" {print $1}' /etc/shadow 2>/dev/null | head -10")
	if execErr != nil || code != 0 {
		// Fall back to /etc/passwd check (less accurate)
		out2, _, _ := exec(ctx, "awk -F: '($2 == \"\") {print $1}' /etc/passwd 2>/dev/null | head -10")
		if strings.TrimSpace(out2) == "" {
			return StatusUnknown, "Could not read /etc/shadow — insufficient privileges. /etc/passwd shows no empty passwords", nil
		}
		return StatusFail, fmt.Sprintf("Passwordless accounts detected: %s", strings.TrimSpace(out2)), nil
	}

	accounts := strings.TrimSpace(out)
	if accounts == "" {
		return StatusPass, "No passwordless user accounts found", nil
	}

	// Filter out locked accounts (!) - those are fine
	lines := strings.Split(accounts, "\n")
	var empty []string
	for _, l := range lines {
		if l != "" {
			empty = append(empty, l)
		}
	}
	if len(empty) == 0 {
		return StatusPass, "No passwordless user accounts found", nil
	}

	return StatusFail, fmt.Sprintf("Passwordless accounts detected: %s", strings.Join(empty, ", ")), nil
}

// checkUnnecessaryServices checks that known-risky services are not running.
func checkUnnecessaryServices(ctx context.Context, exec ProbeExecutor) (status, evidence string, err error) {
	riskyServices := []string{"telnet", "rsh", "rlogin", "rexec", "tftp", "finger", "chargen", "daytime", "echo", "discard"}

	var running []string
	for _, svc := range riskyServices {
		// Check if process is running
		out, code, _ := exec(ctx, fmt.Sprintf("pgrep -x %s 2>/dev/null | wc -l", svc))
		if code == 0 && strings.TrimSpace(out) != "0" {
			running = append(running, svc)
			continue
		}
		// Check via ss/netstat if it's listening
		out2, _, _ := exec(ctx, fmt.Sprintf("ss -tlnp 2>/dev/null | grep '%s' | wc -l", svc))
		if strings.TrimSpace(out2) != "0" {
			running = append(running, svc+"(listening)")
		}
	}

	if len(running) == 0 {
		return StatusPass, fmt.Sprintf("None of the risky services (%s) are running", strings.Join(riskyServices[:5], ", ")+"..."), nil
	}

	return StatusFail, fmt.Sprintf("Risky service(s) detected: %s", strings.Join(running, ", ")), nil
}
