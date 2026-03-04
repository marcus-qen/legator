// Package inventory — Windows inventory collection via WinRM PowerShell.
// ScanWindows collects system information from a remote Windows host using
// PowerShell cmdlets executed over a WinRM connection. No agent binary is
// required on the target host.
package inventory

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

// WindowsPSRunner executes PowerShell on a remote Windows host.
// *executor.WinRMExecutor satisfies this interface via its RunPS method.
type WindowsPSRunner interface {
	RunPS(ctx context.Context, script string) (stdout, stderr string, exitCode int, err error)
}

// ScanWindows collects a full system inventory from a remote Windows host via WinRM.
// It never returns an error for partial data — missing cmdlets or permissions
// result in empty/zero fields rather than failures, keeping the call resilient
// across heterogeneous Windows environments.
func ScanWindows(ctx context.Context, runner WindowsPSRunner, probeID string) (*protocol.InventoryPayload, error) {
	inv := &protocol.InventoryPayload{
		ProbeID:     probeID,
		OS:          "windows",
		Arch:        "amd64", // default; overridden by Get-ComputerInfo if available
		Labels:      map[string]string{},
		Metadata:    map[string]string{"source": "winrm"},
		CollectedAt: time.Now().UTC(),
	}

	collectComputerInfo(ctx, runner, inv)
	inv.Services = collectServices(ctx, runner)
	inv.Packages = collectPackages(ctx, runner)
	inv.Interfaces = collectInterfaces(ctx, runner)

	return inv, nil
}

// ---------------------------------------------------------------------------
// Computer info
// ---------------------------------------------------------------------------

type psComputerInfo struct {
	CsName                string  `json:"CsName"`
	OsName                string  `json:"OsName"`
	OsVersion             string  `json:"OsVersion"`
	OsArchitecture        string  `json:"OsArchitecture"`
	CsNumberOfProcessors  int     `json:"CsNumberOfProcessors"`
	CsTotalPhysicalMemory float64 `json:"CsTotalPhysicalMemory"`
	OsBuildNumber         string  `json:"OsBuildNumber"`
}

func collectComputerInfo(ctx context.Context, runner WindowsPSRunner, inv *protocol.InventoryPayload) {
	const script = `Get-ComputerInfo -Property CsName,OsName,OsVersion,OsArchitecture,CsNumberOfProcessors,CsTotalPhysicalMemory,OsBuildNumber | ConvertTo-Json -Compress`

	stdout, _, exitCode, _ := runner.RunPS(ctx, script)
	if exitCode != 0 || strings.TrimSpace(stdout) == "" {
		// Fallback: collect individual fields when Get-ComputerInfo is unavailable
		// (pre-Windows 10 / Server 2016 without RSAT).
		collectComputerInfoFallback(ctx, runner, inv)
		return
	}

	var info psComputerInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &info); err != nil {
		collectComputerInfoFallback(ctx, runner, inv)
		return
	}

	inv.Hostname = info.CsName
	inv.Kernel = info.OsVersion
	inv.CPUs = info.CsNumberOfProcessors
	inv.MemTotal = uint64(info.CsTotalPhysicalMemory)
	if info.OsArchitecture != "" {
		inv.Arch = mapWindowsArch(info.OsArchitecture)
	}
	if info.OsName != "" {
		inv.Metadata["os_name"] = info.OsName
	}
	if info.OsBuildNumber != "" {
		inv.Metadata["os_build"] = info.OsBuildNumber
	}

	// Disk total via WMI (separate call; Get-ComputerInfo omits it)
	collectDiskTotal(ctx, runner, inv)
}

func collectComputerInfoFallback(ctx context.Context, runner WindowsPSRunner, inv *protocol.InventoryPayload) {
	if h, _, ec, _ := runner.RunPS(ctx, "$env:COMPUTERNAME"); ec == 0 {
		inv.Hostname = strings.TrimSpace(h)
	}
	if k, _, ec, _ := runner.RunPS(ctx, "[System.Environment]::OSVersion.VersionString"); ec == 0 {
		inv.Kernel = strings.TrimSpace(k)
	}
	if c, _, ec, _ := runner.RunPS(ctx, "(Get-CimInstance Win32_ComputerSystem).NumberOfLogicalProcessors"); ec == 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(c)); err == nil {
			inv.CPUs = n
		}
	}
	if m, _, ec, _ := runner.RunPS(ctx, "(Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory"); ec == 0 {
		if n, err := strconv.ParseUint(strings.TrimSpace(m), 10, 64); err == nil {
			inv.MemTotal = n
		}
	}
	collectDiskTotal(ctx, runner, inv)
}

func collectDiskTotal(ctx context.Context, runner WindowsPSRunner, inv *protocol.InventoryPayload) {
	const script = `(Get-CimInstance Win32_LogicalDisk -Filter "DriveType=3" | Measure-Object -Property Size -Sum).Sum`
	if out, _, ec, _ := runner.RunPS(ctx, script); ec == 0 {
		if n, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64); err == nil {
			inv.DiskTotal = n
		}
	}
}

// mapWindowsArch normalises PowerShell architecture strings to GOARCH values.
func mapWindowsArch(arch string) string {
	switch strings.ToLower(arch) {
	case "64-bit", "x64":
		return "amd64"
	case "32-bit", "x86":
		return "386"
	case "arm64":
		return "arm64"
	case "arm":
		return "arm"
	default:
		return strings.ToLower(strings.ReplaceAll(arch, " ", "_"))
	}
}

// ---------------------------------------------------------------------------
// Services
// ---------------------------------------------------------------------------

// psService mirrors the subset of Win32_Service / Get-Service properties we
// collect. Status values: 1=Stopped, 2=StartPending, 3=StopPending, 4=Running,
// 5=ContinuePending, 6=PausePending, 7=Paused.
// StartType values: 0=Boot, 1=System, 2=Automatic, 3=Manual, 4=Disabled.
type psService struct {
	Name      string `json:"Name"`
	Status    int    `json:"Status"`
	StartType int    `json:"StartType"`
}

func collectServices(ctx context.Context, runner WindowsPSRunner) []protocol.Service {
	const script = `Get-Service | Select-Object Name,Status,StartType | ConvertTo-Json -Compress`
	stdout, _, exitCode, _ := runner.RunPS(ctx, script)
	if exitCode != 0 || strings.TrimSpace(stdout) == "" {
		return nil
	}
	raw := strings.TrimSpace(stdout)
	if !strings.HasPrefix(raw, "[") {
		raw = "[" + raw + "]"
	}
	var items []psService
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}

	result := make([]protocol.Service, 0, len(items))
	for _, s := range items {
		state := "stopped"
		if s.Status == 4 {
			state = "running"
		}
		result = append(result, protocol.Service{
			Name:    s.Name,
			State:   state,
			Enabled: s.StartType == 2, // Automatic
		})
	}
	return result
}

// ---------------------------------------------------------------------------
// Packages
// ---------------------------------------------------------------------------

type psPackage struct {
	Name         string `json:"Name"`
	Version      string `json:"Version"`
	ProviderName string `json:"ProviderName"`
}

func collectPackages(ctx context.Context, runner WindowsPSRunner) []protocol.Package {
	const script = `Get-Package | Select-Object Name,Version,ProviderName | ConvertTo-Json -Compress`
	stdout, _, exitCode, _ := runner.RunPS(ctx, script)
	if exitCode != 0 || strings.TrimSpace(stdout) == "" {
		return nil
	}
	raw := strings.TrimSpace(stdout)
	if !strings.HasPrefix(raw, "[") {
		raw = "[" + raw + "]"
	}
	var items []psPackage
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}

	result := make([]protocol.Package, 0, len(items))
	for _, p := range items {
		result = append(result, protocol.Package{
			Name:    p.Name,
			Version: p.Version,
			Manager: p.ProviderName,
		})
	}
	return result
}

// ---------------------------------------------------------------------------
// Network interfaces
// ---------------------------------------------------------------------------

type psIPAddress struct {
	IPAddress    string `json:"IPAddress"`
	PrefixLength int    `json:"PrefixLength"`
}

type psNetAdapter struct {
	MacAddress string `json:"MacAddress"`
	Status     string `json:"Status"` // Up, Down, Disconnected
}

type psNetIPConfig struct {
	InterfaceAlias string        `json:"InterfaceAlias"`
	NetAdapter     psNetAdapter  `json:"NetAdapter"`
	IPv4Address    []psIPAddress `json:"IPv4Address"`
	IPv6Address    []psIPAddress `json:"IPv6Address"`
}

func collectInterfaces(ctx context.Context, runner WindowsPSRunner) []protocol.NetInterface {
	const script = `Get-NetIPConfiguration | Select-Object InterfaceAlias,NetAdapter,IPv4Address,IPv6Address | ConvertTo-Json -Depth 4 -Compress`
	stdout, _, exitCode, _ := runner.RunPS(ctx, script)
	if exitCode != 0 || strings.TrimSpace(stdout) == "" {
		return nil
	}
	raw := strings.TrimSpace(stdout)
	if !strings.HasPrefix(raw, "[") {
		raw = "[" + raw + "]"
	}
	var items []psNetIPConfig
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}

	result := make([]protocol.NetInterface, 0, len(items))
	for _, nic := range items {
		state := "down"
		if strings.EqualFold(nic.NetAdapter.Status, "up") {
			state = "up"
		}
		var addrs []string
		for _, a := range nic.IPv4Address {
			addrs = append(addrs, a.IPAddress+"/"+strconv.Itoa(a.PrefixLength))
		}
		for _, a := range nic.IPv6Address {
			addrs = append(addrs, a.IPAddress+"/"+strconv.Itoa(a.PrefixLength))
		}
		result = append(result, protocol.NetInterface{
			Name:  nic.InterfaceAlias,
			MAC:   nic.NetAdapter.MacAddress,
			Addrs: addrs,
			State: state,
		})
	}
	return result
}
