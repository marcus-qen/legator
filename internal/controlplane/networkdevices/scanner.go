package networkdevices

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Scanner performs multi-command inventory collection on network devices.
type Scanner struct {
	executor *SSHExecutor
	store    *Store
}

// NewScanner creates a Scanner backed by the given executor and store.
func NewScanner(executor *SSHExecutor, store *Store) *Scanner {
	return &Scanner{executor: executor, store: store}
}

// Scan collects device inventory, stores the result, and returns it.
// Non-fatal per-command errors are captured in InventoryResult.Errors.
func (s *Scanner) Scan(ctx context.Context, device Device, creds CredentialInput, cfg ScanConfig) (*InventoryResult, error) {
	vendor := normalizeVendor(device.Vendor)
	commands := scannerVendorCommands(vendor, cfg)

	result := &InventoryResult{
		DeviceID:    device.ID,
		Vendor:      vendor,
		CollectedAt: time.Now().UTC(),
		Raw:         map[string]string{},
		Errors:      []string{},
	}

	for key, cmd := range commands {
		r, err := s.executor.Execute(ctx, device, creds, cmd)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %s", key, err.Error()))
			continue
		}
		if r.Error != "" {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %s", key, r.Error))
		}
		trimmed := strings.TrimSpace(r.Output)
		if trimmed != "" {
			result.Raw[key] = trimmed
		}
		switch key {
		case "hostname":
			result.Hostname = parseHostname(trimmed)
		case "version":
			result.Version = parseScanVersion(trimmed, vendor)
		case "interfaces":
			result.Interfaces = parseInterfaces(trimmed)
		case "serial":
			result.Serial = parseScanSerial(trimmed, vendor)
		}
	}

	if len(result.Errors) == 0 {
		result.Errors = nil
	}
	if len(result.Raw) == 0 {
		result.Raw = nil
	}

	if s.store != nil {
		if saveErr := s.store.SaveInventory(*result); saveErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("storage: %s", saveErr.Error()))
		}
	}

	return result, nil
}

// scannerVendorCommands returns the command set for a given vendor + config.
func scannerVendorCommands(vendor string, cfg ScanConfig) map[string]string {
	var cmds map[string]string
	switch vendor {
	case VendorCisco:
		cmds = map[string]string{
			"hostname":   "show running-config | include ^hostname",
			"version":    "show version",
			"interfaces": "show ip interface brief",
			"serial":     "show version | include Processor board ID",
		}
		if cfg.IncludeRouting {
			cmds["routing"] = "show ip route summary"
		}
	case VendorJunos:
		cmds = map[string]string{
			"hostname":   "show configuration system host-name | display set",
			"version":    "show version",
			"interfaces": "show interfaces terse",
			"serial":     "show chassis hardware | match Chassis",
		}
		if cfg.IncludeRouting {
			cmds["routing"] = "show route summary"
		}
	case VendorFortinet:
		cmds = map[string]string{
			"hostname":   "get system status | grep Hostname",
			"version":    "get system status",
			"interfaces": "get system interface physical",
			"serial":     "get system status | grep Serial",
		}
		if cfg.IncludeRouting {
			cmds["routing"] = "get router info routing-table all"
		}
	default: // VendorGeneric / Linux
		cmds = map[string]string{
			"hostname":   "hostname",
			"version":    "uname -a",
			"interfaces": "ip -brief link show",
			"serial":     "cat /sys/class/dmi/id/product_serial 2>/dev/null || echo unknown",
		}
		if cfg.IncludeRouting {
			cmds["routing"] = "ip route"
		}
	}
	return cmds
}

// parseScanVersion extracts a clean version string from raw command output.
func parseScanVersion(raw, vendor string) string {
	if raw == "" {
		return ""
	}
	switch vendor {
	case VendorCisco:
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Cisco IOS") || strings.Contains(line, "Version") {
				return line
			}
		}
	case VendorJunos:
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "JUNOS") || strings.Contains(line, "JUNOS") {
				return line
			}
		}
	case VendorFortinet:
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(strings.ToLower(line), "version") {
				return line
			}
		}
	}
	return firstLine(raw)
}

// parseScanSerial extracts a serial number from raw command output.
func parseScanSerial(raw, vendor string) string {
	if raw == "" {
		return ""
	}
	raw = strings.TrimSpace(raw)
	switch vendor {
	case VendorCisco:
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "Processor board ID") {
				parts := strings.SplitN(line, "Processor board ID", 2)
				if len(parts) == 2 {
					return strings.TrimSpace(parts[1])
				}
			}
		}
	case VendorJunos:
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Chassis") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					return fields[1]
				}
			}
		}
	case VendorFortinet:
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			lower := strings.ToLower(line)
			if strings.Contains(lower, "serial") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					return strings.TrimSpace(parts[1])
				}
			}
		}
	}
	if raw != "unknown" {
		return firstLine(raw)
	}
	return ""
}
