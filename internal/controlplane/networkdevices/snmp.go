package networkdevices

// SNMP client supporting v2c and v3 for network device inventory collection.
// Uses github.com/gosnmp/gosnmp (pure Go, no CGO).
//
// Standard MIBs queried:
//   - RFC 1213 MIB-II: system group, interfaces group
//   - IF-MIB (RFC 2863): interface counters and status
//   - ENTITY-MIB (RFC 6933): hardware inventory

import (
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Standard OID constants.
const (
	oidSysDescr    = "1.3.6.1.2.1.1.1.0"
	oidSysName     = "1.3.6.1.2.1.1.5.0"
	oidSysLocation = "1.3.6.1.2.1.1.6.0"

	// IF-MIB table base OIDs (without instance)
	oidIfDescr       = "1.3.6.1.2.1.2.2.1.2"
	oidIfSpeed       = "1.3.6.1.2.1.2.2.1.5"
	oidIfPhysAddress = "1.3.6.1.2.1.2.2.1.6"
	oidIfAdminStatus = "1.3.6.1.2.1.2.2.1.7"
	oidIfOperStatus  = "1.3.6.1.2.1.2.2.1.8"
	oidIfInOctets    = "1.3.6.1.2.1.2.2.1.10"
	oidIfOutOctets   = "1.3.6.1.2.1.2.2.1.16"
	oidIfInErrors    = "1.3.6.1.2.1.2.2.1.14"
	oidIfOutErrors   = "1.3.6.1.2.1.2.2.1.20"
)

// SNMPClientInterface defines the SNMP operations used by the enricher.
// Interface-based for easy test mocking.
type SNMPClientInterface interface {
	// GetSystem returns system-level inventory (sysDescr, sysName, sysLocation).
	GetSystem() (*SNMPSystemInfo, error)
	// GetInterfaces returns interface details from IF-MIB.
	GetInterfaces() ([]InterfaceDetail, error)
	// Close releases resources.
	Close() error
}

// SNMPSystemInfo holds data collected from the system MIB group.
type SNMPSystemInfo struct {
	SysDescr    string
	SysName     string
	SysLocation string
}

// snmpClient is the live gosnmp-backed implementation.
type snmpClient struct {
	g *gosnmp.GoSNMP
}

// NewSNMPClient creates and connects an SNMP client from the given config.
func NewSNMPClient(cfg SNMPConfig) (SNMPClientInterface, error) {
	port := cfg.Port
	if port == 0 {
		port = 161
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	retries := cfg.Retries
	if retries <= 0 {
		retries = 2
	}

	g := &gosnmp.GoSNMP{
		Target:    cfg.Host,
		Port:      port,
		Timeout:   timeout,
		Retries:   retries,
		MaxOids:   60,
		Transport: "udp",
	}

	switch cfg.Version {
	case SNMPv3:
		g.Version = gosnmp.Version3
		g.SecurityModel = gosnmp.UserSecurityModel
		msgFlags := gosnmp.NoAuthNoPriv
		usmParams := &gosnmp.UsmSecurityParameters{
			UserName: cfg.Username,
		}
		switch cfg.AuthProtocol {
		case SNMPAuthMD5:
			usmParams.AuthenticationProtocol = gosnmp.MD5
			usmParams.AuthenticationPassphrase = cfg.AuthPassword
			msgFlags = gosnmp.AuthNoPriv
		case SNMPAuthSHA:
			usmParams.AuthenticationProtocol = gosnmp.SHA
			usmParams.AuthenticationPassphrase = cfg.AuthPassword
			msgFlags = gosnmp.AuthNoPriv
		}
		switch cfg.PrivProtocol {
		case SNMPPrivDES:
			usmParams.PrivacyProtocol = gosnmp.DES
			usmParams.PrivacyPassphrase = cfg.PrivPassword
			msgFlags = gosnmp.AuthPriv
		case SNMPPrivAES:
			usmParams.PrivacyProtocol = gosnmp.AES
			usmParams.PrivacyPassphrase = cfg.PrivPassword
			msgFlags = gosnmp.AuthPriv
		}
		g.MsgFlags = msgFlags
		g.SecurityParameters = usmParams
	default:
		// v2c
		g.Version = gosnmp.Version2c
		community := cfg.Community
		if community == "" {
			community = "public"
		}
		g.Community = community
	}

	if err := g.Connect(); err != nil {
		return nil, fmt.Errorf("snmp connect to %s:%d: %w", cfg.Host, port, err)
	}
	return &snmpClient{g: g}, nil
}

// Close releases the SNMP UDP connection.
func (c *snmpClient) Close() error {
	if c.g != nil && c.g.Conn != nil {
		return c.g.Conn.Close()
	}
	return nil
}

// GetSystem fetches sysDescr, sysName, sysLocation via SNMP GET.
func (c *snmpClient) GetSystem() (*SNMPSystemInfo, error) {
	oids := []string{oidSysDescr, oidSysName, oidSysLocation}
	result, err := c.g.Get(oids)
	if err != nil {
		return nil, fmt.Errorf("snmp get system: %w", err)
	}
	info := &SNMPSystemInfo{}
	for _, v := range result.Variables {
		val := snmpStringValue(v)
		switch v.Name {
		case "." + oidSysDescr, oidSysDescr:
			info.SysDescr = val
		case "." + oidSysName, oidSysName:
			info.SysName = val
		case "." + oidSysLocation, oidSysLocation:
			info.SysLocation = val
		}
	}
	return info, nil
}

// GetInterfaces walks IF-MIB to build per-interface detail records.
func (c *snmpClient) GetInterfaces() ([]InterfaceDetail, error) {
	// Walk ifDescr to enumerate interfaces
	descrMap, err := c.walkTable(oidIfDescr)
	if err != nil {
		return nil, fmt.Errorf("snmp walk ifDescr: %w", err)
	}
	if len(descrMap) == 0 {
		return nil, nil
	}

	// Walk remaining columns
	speedMap, _ := c.walkTable(oidIfSpeed)
	macMap, _ := c.walkTable(oidIfPhysAddress)
	adminMap, _ := c.walkTable(oidIfAdminStatus)
	operMap, _ := c.walkTable(oidIfOperStatus)
	inOctMap, _ := c.walkTable(oidIfInOctets)
	outOctMap, _ := c.walkTable(oidIfOutOctets)
	inErrMap, _ := c.walkTable(oidIfInErrors)
	outErrMap, _ := c.walkTable(oidIfOutErrors)

	out := make([]InterfaceDetail, 0, len(descrMap))
	for idx, descr := range descrMap {
		detail := InterfaceDetail{
			Index:      idx,
			Name:       descr,
			AdminUp:    snmpStatusUp(adminMap[idx]),
			OperUp:     snmpStatusUp(operMap[idx]),
			SpeedMbps:  snmpUint64Value(speedMap[idx]) / 1_000_000,
			MACAddress: snmpMACAddress(macMap[idx]),
			InOctets:   snmpUint64Value(inOctMap[idx]),
			OutOctets:  snmpUint64Value(outOctMap[idx]),
			InErrors:   snmpUint64Value(inErrMap[idx]),
			OutErrors:  snmpUint64Value(outErrMap[idx]),
		}
		out = append(out, detail)
	}
	return out, nil
}

// walkTable does an SNMP WALK on a table base OID and returns a map of
// ifIndex (integer) → string value.
func (c *snmpClient) walkTable(baseOID string) (map[int]string, error) {
	result, err := c.g.WalkAll(baseOID)
	if err != nil {
		return nil, err
	}
	m := make(map[int]string, len(result))
	for _, v := range result {
		idx := snmpTableIndex(v.Name, baseOID)
		if idx < 0 {
			continue
		}
		m[idx] = snmpRawValue(v)
	}
	return m, nil
}

// --- value helpers ---

// snmpTableIndex extracts the last integer component from an OID.
// e.g. ".1.3.6.1.2.1.2.2.1.2.3" with base "1.3.6.1.2.1.2.2.1.2" → 3.
func snmpTableIndex(oid, base string) int {
	base = strings.TrimPrefix(base, ".")
	oid = strings.TrimPrefix(oid, ".")
	if !strings.HasPrefix(oid, base) {
		return -1
	}
	suffix := strings.TrimPrefix(oid, base)
	suffix = strings.TrimPrefix(suffix, ".")
	if suffix == "" {
		return -1
	}
	var idx int
	// only single-component index (ifIndex is an integer)
	if _, err := fmt.Sscanf(suffix, "%d", &idx); err != nil {
		return -1
	}
	return idx
}

// snmpStringValue converts an SNMP variable to a printable string.
func snmpStringValue(v gosnmp.SnmpPDU) string {
	switch v.Type {
	case gosnmp.OctetString:
		b, ok := v.Value.([]byte)
		if !ok {
			return fmt.Sprintf("%v", v.Value)
		}
		// Check if the bytes are printable ASCII
		if isPrintableASCII(b) {
			return strings.TrimSpace(string(b))
		}
		return hex.EncodeToString(b)
	default:
		return fmt.Sprintf("%v", v.Value)
	}
}

// snmpRawValue returns a string representation suitable for map storage.
func snmpRawValue(v gosnmp.SnmpPDU) string {
	return snmpStringValue(v)
}

// snmpUint64Value extracts a numeric value as uint64.
func snmpUint64Value(s string) uint64 {
	if s == "" {
		return 0
	}
	var n uint64
	fmt.Sscanf(s, "%d", &n)
	return n
}

// snmpStatusUp returns true if the IF-MIB status value indicates "up" (1).
func snmpStatusUp(s string) bool {
	return s == "1"
}

// snmpMACAddress formats a MAC address from a hex string.
func snmpMACAddress(s string) string {
	if len(s) == 12 {
		// Already hex, format as XX:XX:XX:XX:XX:XX
		return fmt.Sprintf("%s:%s:%s:%s:%s:%s",
			s[0:2], s[2:4], s[4:6], s[6:8], s[8:10], s[10:12])
	}
	return s
}

// isPrintableASCII returns true if all bytes are printable ASCII (32–126).
func isPrintableASCII(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	for _, c := range b {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}

// ParseSNMPFirmware tries to extract a firmware/version string from sysDescr.
// Different vendors embed version info differently.
func ParseSNMPFirmware(sysDescr, vendor string) string {
	if sysDescr == "" {
		return ""
	}
	lower := strings.ToLower(sysDescr)
	switch vendor {
	case VendorCisco:
		// "Cisco IOS Software, Version 15.1(4)M12a, ..."
		for _, line := range strings.Split(sysDescr, ",") {
			line = strings.TrimSpace(line)
			if strings.Contains(strings.ToLower(line), "version") {
				return line
			}
		}
	case VendorJunos:
		// "Juniper Networks, Inc. ex2200-48t-4g, JUNOS 18.2R3-S3.1, ..."
		for _, part := range strings.Split(sysDescr, ",") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(strings.ToLower(part), "junos") {
				return part
			}
		}
	case VendorFortinet:
		for _, line := range strings.Split(sysDescr, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(strings.ToLower(line), "fortios") ||
				strings.Contains(strings.ToLower(line), "version") {
				return line
			}
		}
	}
	// Generic: look for "Version" keyword
	for _, part := range strings.FieldsFunc(sysDescr, func(r rune) bool { return r == ',' || r == ';' }) {
		part = strings.TrimSpace(part)
		if strings.Contains(strings.ToLower(part), "version") {
			return part
		}
	}
	// Fall back to raw sysDescr first line
	_ = lower
	return firstLine(sysDescr)
}

// NetconfResult holds data collected via NETCONF.
type NetconfResult struct {
	Firmware   string
	Interfaces []InterfaceDetail
	Errors     []string
}

// SNMPResult holds data collected via SNMP.
type SNMPResult struct {
	SysDescr    string
	SysName     string
	SysLocation string
	Firmware    string
	Interfaces  []InterfaceDetail
	Errors      []string
}

// formatMACFromBytes converts raw bytes to MAC address string.
func formatMACFromBytes(b []byte) string {
	if len(b) != 6 {
		return hex.EncodeToString(b)
	}
	return net.HardwareAddr(b).String()
}
