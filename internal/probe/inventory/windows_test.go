package inventory

import (
	"context"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
)

// ---------------------------------------------------------------------------
// Mock WindowsPSRunner
// ---------------------------------------------------------------------------

type mockWindowsPSRunner struct {
	// responses maps a keyword (substring of script) to a canned response.
	responses map[string]struct {
		stdout   string
		exitCode int
	}
}

func (m *mockWindowsPSRunner) RunPS(_ context.Context, script string) (string, string, int, error) {
	for keyword, resp := range m.responses {
		if strings.Contains(script, keyword) {
			return resp.stdout, "", resp.exitCode, nil
		}
	}
	return "", "", 1, nil // unmatched: exit 1 (simulates cmdlet not found)
}

// ---------------------------------------------------------------------------
// mapWindowsArch
// ---------------------------------------------------------------------------

func TestMapWindowsArch(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"64-bit", "amd64"},
		{"x64", "amd64"},
		{"32-bit", "386"},
		{"x86", "386"},
		{"arm64", "arm64"},
		{"ARM64", "arm64"},
		{"ARM", "arm"},
		{"mips", "mips"},
	}
	for _, tc := range cases {
		if got := mapWindowsArch(tc.in); got != tc.want {
			t.Errorf("mapWindowsArch(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Computer info parsing
// ---------------------------------------------------------------------------

const sampleComputerInfoJSON = `{"CsName":"WIN-DC01","OsName":"Microsoft Windows Server 2022 Datacenter","OsVersion":"10.0.20348","OsArchitecture":"64-bit","CsNumberOfProcessors":4,"CsTotalPhysicalMemory":8589934592,"OsBuildNumber":"20348"}`

func TestCollectComputerInfo_ParsesJSON(t *testing.T) {
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-ComputerInfo":  {stdout: sampleComputerInfoJSON, exitCode: 0},
			"Win32_LogicalDisk": {stdout: "107374182400\n", exitCode: 0},
		},
	}

	inv := newTestInventory("probe-1")
	collectComputerInfo(context.Background(), runner, inv)

	if inv.Hostname != "WIN-DC01" {
		t.Errorf("expected hostname WIN-DC01, got %q", inv.Hostname)
	}
	if inv.Kernel != "10.0.20348" {
		t.Errorf("expected kernel 10.0.20348, got %q", inv.Kernel)
	}
	if inv.CPUs != 4 {
		t.Errorf("expected 4 CPUs, got %d", inv.CPUs)
	}
	if inv.MemTotal != 8589934592 {
		t.Errorf("expected 8GiB mem, got %d", inv.MemTotal)
	}
	if inv.Arch != "amd64" {
		t.Errorf("expected amd64, got %q", inv.Arch)
	}
	if inv.Metadata["os_build"] != "20348" {
		t.Errorf("expected os_build 20348, got %q", inv.Metadata["os_build"])
	}
	if inv.DiskTotal != 107374182400 {
		t.Errorf("expected disk total 107374182400, got %d", inv.DiskTotal)
	}
}

func TestCollectComputerInfo_FallsBackOnError(t *testing.T) {
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-ComputerInfo":          {stdout: "", exitCode: 1}, // cmdlet not available
			"COMPUTERNAME":              {stdout: "LEGACY-HOST\n", exitCode: 0},
			"OSVersion":                 {stdout: "Microsoft Windows NT 6.1.7601\n", exitCode: 0},
			"NumberOfLogicalProcessors": {stdout: "2\n", exitCode: 0},
			"TotalPhysicalMemory":       {stdout: "4294967296\n", exitCode: 0},
			"Win32_LogicalDisk":         {stdout: "53687091200\n", exitCode: 0},
		},
	}

	inv := newTestInventory("probe-2")
	collectComputerInfo(context.Background(), runner, inv)

	if inv.Hostname != "LEGACY-HOST" {
		t.Errorf("expected fallback hostname LEGACY-HOST, got %q", inv.Hostname)
	}
	if inv.CPUs != 2 {
		t.Errorf("expected 2 CPUs from fallback, got %d", inv.CPUs)
	}
	if inv.MemTotal != 4294967296 {
		t.Errorf("expected 4GiB from fallback, got %d", inv.MemTotal)
	}
	if inv.DiskTotal != 53687091200 {
		t.Errorf("expected disk from fallback, got %d", inv.DiskTotal)
	}
}

func TestCollectComputerInfo_InvalidJSONFallsBack(t *testing.T) {
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-ComputerInfo": {stdout: "not-json!!!", exitCode: 0},
			"COMPUTERNAME":     {stdout: "JSON-FAIL\n", exitCode: 0},
		},
	}

	inv := newTestInventory("probe-3")
	collectComputerInfo(context.Background(), runner, inv)

	if inv.Hostname != "JSON-FAIL" {
		t.Errorf("expected JSON-FAIL hostname after fallback, got %q", inv.Hostname)
	}
}

// ---------------------------------------------------------------------------
// Service parsing
// ---------------------------------------------------------------------------

const sampleServicesJSON = `[{"Name":"wuauserv","Status":4,"StartType":2},{"Name":"Spooler","Status":4,"StartType":2},{"Name":"AudioSrv","Status":1,"StartType":3}]`

func TestCollectServices_ParsesJSON(t *testing.T) {
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-Service": {stdout: sampleServicesJSON, exitCode: 0},
		},
	}

	svcs := collectServices(context.Background(), runner)
	if len(svcs) != 3 {
		t.Fatalf("expected 3 services, got %d", len(svcs))
	}

	// wuauserv: running, automatic → enabled
	if svcs[0].Name != "wuauserv" {
		t.Errorf("expected wuauserv, got %q", svcs[0].Name)
	}
	if svcs[0].State != "running" {
		t.Errorf("expected running, got %q", svcs[0].State)
	}
	if !svcs[0].Enabled {
		t.Error("expected wuauserv enabled=true (StartType=2)")
	}

	// AudioSrv: stopped, manual → not enabled
	if svcs[2].Name != "AudioSrv" {
		t.Errorf("expected AudioSrv, got %q", svcs[2].Name)
	}
	if svcs[2].State != "stopped" {
		t.Errorf("expected stopped, got %q", svcs[2].State)
	}
	if svcs[2].Enabled {
		t.Error("expected AudioSrv enabled=false (StartType=3)")
	}
}

func TestCollectServices_SingleObjectWrapped(t *testing.T) {
	// PowerShell emits a plain object (not array) when only one service matches.
	const single = `{"Name":"WinRM","Status":4,"StartType":2}`
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-Service": {stdout: single, exitCode: 0},
		},
	}

	svcs := collectServices(context.Background(), runner)
	if len(svcs) != 1 {
		t.Fatalf("expected 1 service, got %d", len(svcs))
	}
	if svcs[0].Name != "WinRM" {
		t.Errorf("expected WinRM, got %q", svcs[0].Name)
	}
	if svcs[0].State != "running" {
		t.Errorf("expected running, got %q", svcs[0].State)
	}
}

func TestCollectServices_EmptyOnFailedExitCode(t *testing.T) {
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-Service": {stdout: "", exitCode: 1},
		},
	}
	if svcs := collectServices(context.Background(), runner); svcs != nil {
		t.Fatalf("expected nil on error exit, got %v", svcs)
	}
}

func TestCollectServices_InvalidJSONReturnsNil(t *testing.T) {
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-Service": {stdout: "garbage-output", exitCode: 0},
		},
	}
	if svcs := collectServices(context.Background(), runner); svcs != nil {
		t.Fatalf("expected nil on parse error, got %v", svcs)
	}
}

// ---------------------------------------------------------------------------
// Package parsing
// ---------------------------------------------------------------------------

const samplePackagesJSON = `[{"Name":"7-Zip 22.01","Version":"22.01","ProviderName":"Programs"},{"Name":"Git","Version":"2.41.0","ProviderName":"Programs"}]`

func TestCollectPackages_ParsesJSON(t *testing.T) {
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-Package": {stdout: samplePackagesJSON, exitCode: 0},
		},
	}

	pkgs := collectPackages(context.Background(), runner)
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(pkgs))
	}
	if pkgs[0].Name != "7-Zip 22.01" {
		t.Errorf("expected 7-Zip 22.01, got %q", pkgs[0].Name)
	}
	if pkgs[0].Version != "22.01" {
		t.Errorf("expected version 22.01, got %q", pkgs[0].Version)
	}
	if pkgs[0].Manager != "Programs" {
		t.Errorf("expected manager Programs, got %q", pkgs[0].Manager)
	}
}

func TestCollectPackages_EmptyOnError(t *testing.T) {
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-Package": {stdout: "", exitCode: 1},
		},
	}
	if pkgs := collectPackages(context.Background(), runner); pkgs != nil {
		t.Fatalf("expected nil on error, got %v", pkgs)
	}
}

func TestCollectPackages_SingleObjectWrapped(t *testing.T) {
	const single = `{"Name":"PowerShell 7","Version":"7.3.0","ProviderName":"msi"}`
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-Package": {stdout: single, exitCode: 0},
		},
	}

	pkgs := collectPackages(context.Background(), runner)
	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package, got %d", len(pkgs))
	}
	if pkgs[0].Name != "PowerShell 7" {
		t.Errorf("expected PowerShell 7, got %q", pkgs[0].Name)
	}
}

// ---------------------------------------------------------------------------
// Network interface parsing
// ---------------------------------------------------------------------------

const sampleNetIPConfigJSON = `[{"InterfaceAlias":"Ethernet","NetAdapter":{"MacAddress":"00-1A-2B-3C-4D-5E","Status":"Up"},"IPv4Address":[{"IPAddress":"192.168.1.100","PrefixLength":24}],"IPv6Address":[{"IPAddress":"fe80::1","PrefixLength":64}]},{"InterfaceAlias":"Loopback Pseudo-Interface 1","NetAdapter":{"MacAddress":"","Status":"Up"},"IPv4Address":[{"IPAddress":"127.0.0.1","PrefixLength":8}],"IPv6Address":[]}]`

func TestCollectInterfaces_ParsesJSON(t *testing.T) {
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-NetIPConfiguration": {stdout: sampleNetIPConfigJSON, exitCode: 0},
		},
	}

	nics := collectInterfaces(context.Background(), runner)
	if len(nics) != 2 {
		t.Fatalf("expected 2 interfaces, got %d", len(nics))
	}

	eth := nics[0]
	if eth.Name != "Ethernet" {
		t.Errorf("expected Ethernet, got %q", eth.Name)
	}
	if eth.State != "up" {
		t.Errorf("expected state up, got %q", eth.State)
	}
	if eth.MAC != "00-1A-2B-3C-4D-5E" {
		t.Errorf("expected MAC 00-1A-2B-3C-4D-5E, got %q", eth.MAC)
	}
	if len(eth.Addrs) != 2 {
		t.Errorf("expected 2 addresses (ipv4+ipv6), got %d: %v", len(eth.Addrs), eth.Addrs)
	}
	if eth.Addrs[0] != "192.168.1.100/24" {
		t.Errorf("expected 192.168.1.100/24, got %q", eth.Addrs[0])
	}
	if eth.Addrs[1] != "fe80::1/64" {
		t.Errorf("expected fe80::1/64, got %q", eth.Addrs[1])
	}
}

func TestCollectInterfaces_EmptyOnError(t *testing.T) {
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-NetIPConfiguration": {stdout: "", exitCode: 1},
		},
	}
	if nics := collectInterfaces(context.Background(), runner); nics != nil {
		t.Fatalf("expected nil, got %v", nics)
	}
}

func TestCollectInterfaces_SingleObjectWrapped(t *testing.T) {
	const single = `{"InterfaceAlias":"Wi-Fi","NetAdapter":{"MacAddress":"AA-BB-CC-DD-EE-FF","Status":"Up"},"IPv4Address":[{"IPAddress":"10.0.0.5","PrefixLength":24}],"IPv6Address":[]}`
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-NetIPConfiguration": {stdout: single, exitCode: 0},
		},
	}

	nics := collectInterfaces(context.Background(), runner)
	if len(nics) != 1 {
		t.Fatalf("expected 1 interface, got %d", len(nics))
	}
	if nics[0].Name != "Wi-Fi" {
		t.Errorf("expected Wi-Fi, got %q", nics[0].Name)
	}
	if len(nics[0].Addrs) != 1 {
		t.Errorf("expected 1 address, got %d", len(nics[0].Addrs))
	}
}

// ---------------------------------------------------------------------------
// ScanWindows integration
// ---------------------------------------------------------------------------

func TestScanWindows_PopulatesAllFields(t *testing.T) {
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-ComputerInfo":       {stdout: sampleComputerInfoJSON, exitCode: 0},
			"Win32_LogicalDisk":      {stdout: "214748364800\n", exitCode: 0},
			"Get-Service":            {stdout: sampleServicesJSON, exitCode: 0},
			"Get-Package":            {stdout: samplePackagesJSON, exitCode: 0},
			"Get-NetIPConfiguration": {stdout: sampleNetIPConfigJSON, exitCode: 0},
		},
	}

	inv, err := ScanWindows(context.Background(), runner, "win-probe-1")
	if err != nil {
		t.Fatalf("ScanWindows returned error: %v", err)
	}
	if inv.ProbeID != "win-probe-1" {
		t.Errorf("expected probe ID win-probe-1, got %q", inv.ProbeID)
	}
	if inv.OS != "windows" {
		t.Errorf("expected OS windows, got %q", inv.OS)
	}
	if inv.Hostname != "WIN-DC01" {
		t.Errorf("expected hostname WIN-DC01, got %q", inv.Hostname)
	}
	if len(inv.Services) != 3 {
		t.Errorf("expected 3 services, got %d", len(inv.Services))
	}
	if len(inv.Packages) != 2 {
		t.Errorf("expected 2 packages, got %d", len(inv.Packages))
	}
	if len(inv.Interfaces) != 2 {
		t.Errorf("expected 2 interfaces, got %d", len(inv.Interfaces))
	}
	if inv.CollectedAt.IsZero() {
		t.Error("CollectedAt should not be zero")
	}
	if inv.Metadata["source"] != "winrm" {
		t.Errorf("expected source=winrm metadata, got %q", inv.Metadata["source"])
	}
	if inv.DiskTotal != 214748364800 {
		t.Errorf("expected disk 214748364800, got %d", inv.DiskTotal)
	}
}

func TestScanWindows_ResilientToPartialFailures(t *testing.T) {
	// Only ComputerInfo and disk succeed; everything else fails.
	runner := &mockWindowsPSRunner{
		responses: map[string]struct {
			stdout   string
			exitCode int
		}{
			"Get-ComputerInfo":  {stdout: sampleComputerInfoJSON, exitCode: 0},
			"Win32_LogicalDisk": {stdout: "0\n", exitCode: 0},
			// All other scripts → exit 1
		},
	}

	inv, err := ScanWindows(context.Background(), runner, "probe-partial")
	if err != nil {
		t.Fatalf("ScanWindows should not return error on partial failure: %v", err)
	}
	if inv.Hostname == "" {
		t.Error("hostname should be populated from ComputerInfo")
	}
	if inv.Services != nil {
		t.Errorf("expected nil services on failure, got %v", inv.Services)
	}
	if inv.Packages != nil {
		t.Errorf("expected nil packages on failure, got %v", inv.Packages)
	}
}

func TestScanWindows_PreservesProbeID(t *testing.T) {
	runner := &mockWindowsPSRunner{responses: map[string]struct {
		stdout   string
		exitCode int
	}{}}

	inv, err := ScanWindows(context.Background(), runner, "probe-abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inv.ProbeID != "probe-abc" {
		t.Errorf("expected probe-abc, got %q", inv.ProbeID)
	}
	if inv.OS != "windows" {
		t.Errorf("expected OS=windows, got %q", inv.OS)
	}
}

// ---------------------------------------------------------------------------
// helper
// ---------------------------------------------------------------------------

func newTestInventory(probeID string) *protocol.InventoryPayload {
	return &protocol.InventoryPayload{
		ProbeID:  probeID,
		Labels:   map[string]string{},
		Metadata: map[string]string{},
	}
}
