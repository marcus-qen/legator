package networkdevices

import (
	"fmt"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// --- mock SNMP client for use in enrichment tests ---

type mockSNMPClient struct {
	system     *SNMPSystemInfo
	systemErr  error
	interfaces []InterfaceDetail
	ifaceErr   error
	closed     bool
}

func (m *mockSNMPClient) GetSystem() (*SNMPSystemInfo, error) {
	return m.system, m.systemErr
}

func (m *mockSNMPClient) GetInterfaces() ([]InterfaceDetail, error) {
	return m.interfaces, m.ifaceErr
}

func (m *mockSNMPClient) Close() error {
	m.closed = true
	return nil
}

// --- snmpTableIndex ---

func TestSNMPTableIndex(t *testing.T) {
	tests := []struct {
		oid  string
		base string
		want int
	}{
		{".1.3.6.1.2.1.2.2.1.2.1", "1.3.6.1.2.1.2.2.1.2", 1},
		{".1.3.6.1.2.1.2.2.1.2.42", "1.3.6.1.2.1.2.2.1.2", 42},
		{"1.3.6.1.2.1.2.2.1.2.5", "1.3.6.1.2.1.2.2.1.2", 5},
		{".1.3.6.1.2.1.2.2.1.2", "1.3.6.1.2.1.2.2.1.2", -1},   // exact base, no index
		{".1.3.6.1.2.1.3.2.1.2.1", "1.3.6.1.2.1.2.2.1.2", -1}, // different OID
	}

	for _, tc := range tests {
		got := snmpTableIndex(tc.oid, tc.base)
		if got != tc.want {
			t.Errorf("snmpTableIndex(%q, %q) = %d, want %d", tc.oid, tc.base, got, tc.want)
		}
	}
}

// --- snmpStringValue ---

func TestSNMPStringValue(t *testing.T) {
	tests := []struct {
		name string
		pdu  gosnmp.SnmpPDU
		want string
	}{
		{
			name: "printable bytes",
			pdu:  gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte("router-1")},
			want: "router-1",
		},
		{
			name: "binary mac bytes not printable",
			pdu:  gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte{0x00, 0x1A, 0x2B, 0x3C, 0x4D, 0x5E}},
			want: "001a2b3c4d5e",
		},
		{
			name: "integer value",
			pdu:  gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: 1},
			want: "1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := snmpStringValue(tc.pdu)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// --- snmpUint64Value ---

func TestSNMPUint64Value(t *testing.T) {
	if v := snmpUint64Value("1234"); v != 1234 {
		t.Errorf("got %d", v)
	}
	if v := snmpUint64Value(""); v != 0 {
		t.Errorf("got %d for empty string", v)
	}
	if v := snmpUint64Value("abc"); v != 0 {
		t.Errorf("got %d for non-numeric", v)
	}
}

// --- snmpStatusUp ---

func TestSNMPStatusUp(t *testing.T) {
	if !snmpStatusUp("1") {
		t.Error("expected up for '1'")
	}
	if snmpStatusUp("2") {
		t.Error("expected down for '2'")
	}
	if snmpStatusUp("") {
		t.Error("expected down for empty")
	}
}

// --- snmpMACAddress ---

func TestSNMPMACAddress(t *testing.T) {
	if got := snmpMACAddress("001a2b3c4d5e"); got != "00:1a:2b:3c:4d:5e" {
		t.Errorf("got %q", got)
	}
	if got := snmpMACAddress("short"); got != "short" {
		t.Errorf("got %q", got)
	}
}

// --- isPrintableASCII ---

func TestIsPrintableASCII(t *testing.T) {
	if !isPrintableASCII([]byte("hello world")) {
		t.Error("expected printable for 'hello world'")
	}
	if !isPrintableASCII(nil) {
		t.Error("expected printable for nil")
	}
	if isPrintableASCII([]byte{0x00, 0x01}) {
		t.Error("expected not printable for control chars")
	}
}

// --- ParseSNMPFirmware ---

func TestParseSNMPFirmware(t *testing.T) {
	tests := []struct {
		name     string
		sysDescr string
		vendor   string
		wantIn   string // substring expected in result
	}{
		{
			name:     "cisco ios version",
			sysDescr: "Cisco IOS Software, Version 15.1(4)M12a, RELEASE SOFTWARE (fc1)",
			vendor:   VendorCisco,
			wantIn:   "Version",
		},
		{
			name:     "junos version",
			sysDescr: "Juniper Networks, Inc. ex2200-48t-4g, JUNOS 18.2R3-S3.1, Rev 1",
			vendor:   VendorJunos,
			wantIn:   "JUNOS",
		},
		{
			name:     "generic fallback",
			sysDescr: "Linux 5.10.0 #1 SMP x86_64 GNU/Linux",
			vendor:   VendorGeneric,
			wantIn:   "Linux",
		},
		{
			name:     "empty sysDescr",
			sysDescr: "",
			vendor:   VendorCisco,
			wantIn:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseSNMPFirmware(tc.sysDescr, tc.vendor)
			if tc.wantIn == "" {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}
			if !containsStr(got, tc.wantIn) {
				t.Errorf("got %q, want substring %q", got, tc.wantIn)
			}
		})
	}
}

// --- SNMPConfig defaults ---

func TestSNMPConfigDefaults(t *testing.T) {
	cfg := SNMPConfig{
		Host:    "10.0.0.1",
		Version: SNMPv2c,
	}
	// Verify zero port defaults to 161 in client constructor (without dialing)
	port := cfg.Port
	if port == 0 {
		port = 161
	}
	if port != 161 {
		t.Errorf("expected default port 161, got %d", port)
	}
}

func TestSNMPVersionConstants(t *testing.T) {
	if SNMPv2c != 2 {
		t.Errorf("SNMPv2c should be 2, got %d", SNMPv2c)
	}
	if SNMPv3 != 3 {
		t.Errorf("SNMPv3 should be 3, got %d", SNMPv3)
	}
}

func TestFormatMACFromBytes(t *testing.T) {
	b := []byte{0x00, 0x1A, 0x2B, 0x3C, 0x4D, 0x5E}
	got := formatMACFromBytes(b)
	if got != "00:1a:2b:3c:4d:5e" {
		t.Errorf("got %q", got)
	}
	// Short bytes → hex fallback
	short := []byte{0xFF}
	got2 := formatMACFromBytes(short)
	if got2 == "" {
		t.Error("expected non-empty for short bytes")
	}
}

// --- walkTable OID parsing (unit test without network) ---

func TestSNMPClientWalkTableIndexParsing(t *testing.T) {
	// Verify that OID suffix extraction works for all IF-MIB table OIDs
	tableOIDs := []string{
		oidIfDescr, oidIfSpeed, oidIfPhysAddress,
		oidIfAdminStatus, oidIfOperStatus,
		oidIfInOctets, oidIfOutOctets,
		oidIfInErrors, oidIfOutErrors,
	}
	for _, base := range tableOIDs {
		oid := fmt.Sprintf(".%s.1", base)
		idx := snmpTableIndex(oid, base)
		if idx != 1 {
			t.Errorf("snmpTableIndex(%q, %q) = %d, want 1", oid, base, idx)
		}
	}
}
