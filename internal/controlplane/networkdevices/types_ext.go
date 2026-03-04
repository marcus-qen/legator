package networkdevices

import "time"

// --- NETCONF types ---

// NetconfConfig holds connection parameters for a NETCONF session.
type NetconfConfig struct {
	Host       string        `json:"host"`
	Port       int           `json:"port,omitempty"` // default 830
	Username   string        `json:"username"`
	Password   string        `json:"password,omitempty"`
	PrivateKey string        `json:"private_key,omitempty"`
	Timeout    time.Duration `json:"-"`
}

// --- SNMP types ---

// SNMPVersion identifies the SNMP protocol version.
type SNMPVersion int

const (
	SNMPv2c SNMPVersion = 2
	SNMPv3  SNMPVersion = 3
)

// SNMPAuthProtocol identifies the SNMPv3 authentication protocol.
type SNMPAuthProtocol string

const (
	SNMPAuthNone SNMPAuthProtocol = "none"
	SNMPAuthMD5  SNMPAuthProtocol = "md5"
	SNMPAuthSHA  SNMPAuthProtocol = "sha"
)

// SNMPPrivProtocol identifies the SNMPv3 privacy (encryption) protocol.
type SNMPPrivProtocol string

const (
	SNMPPrivNone SNMPPrivProtocol = "none"
	SNMPPrivDES  SNMPPrivProtocol = "des"
	SNMPPrivAES  SNMPPrivProtocol = "aes"
)

// SNMPConfig holds parameters for SNMP queries.
type SNMPConfig struct {
	Host      string        `json:"host"`
	Port      uint16        `json:"port,omitempty"`      // default 161
	Version   SNMPVersion   `json:"version"`             // 2 or 3
	Community string        `json:"community,omitempty"` // v2c
	Timeout   time.Duration `json:"-"`
	Retries   int           `json:"retries,omitempty"`

	// SNMPv3 fields
	Username     string           `json:"username,omitempty"`
	AuthProtocol SNMPAuthProtocol `json:"auth_protocol,omitempty"`
	AuthPassword string           `json:"auth_password,omitempty"`
	PrivProtocol SNMPPrivProtocol `json:"priv_protocol,omitempty"`
	PrivPassword string           `json:"priv_password,omitempty"`
}

// --- Enriched inventory types ---

// InterfaceDetail holds rich per-interface data collected via SNMP/NETCONF.
type InterfaceDetail struct {
	Index       int    `json:"index,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MACAddress  string `json:"mac_address,omitempty"`
	SpeedMbps   uint64 `json:"speed_mbps,omitempty"`
	AdminUp     bool   `json:"admin_up"`
	OperUp      bool   `json:"oper_up"`
	InOctets    uint64 `json:"in_octets,omitempty"`
	OutOctets   uint64 `json:"out_octets,omitempty"`
	InErrors    uint64 `json:"in_errors,omitempty"`
	OutErrors   uint64 `json:"out_errors,omitempty"`
	VLAN        int    `json:"vlan,omitempty"`
}

// VLANInfo holds a discovered VLAN entry.
type VLANInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name,omitempty"`
}

// RouteEntry holds a routing table entry.
type RouteEntry struct {
	Destination string `json:"destination"`
	Prefix      int    `json:"prefix,omitempty"`
	NextHop     string `json:"next_hop,omitempty"`
	Metric      int    `json:"metric,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
}

// EnrichedInventory is the result of a NETCONF+SNMP enrichment pass.
type EnrichedInventory struct {
	DeviceID    string            `json:"device_id"`
	CollectedAt time.Time         `json:"collected_at"`
	Hostname    string            `json:"hostname,omitempty"`
	Vendor      string            `json:"vendor,omitempty"`
	Firmware    string            `json:"firmware,omitempty"`
	Serial      string            `json:"serial,omitempty"`
	SysDescr    string            `json:"sys_descr,omitempty"`
	SysLocation string            `json:"sys_location,omitempty"`
	Interfaces  []InterfaceDetail `json:"interfaces,omitempty"`
	VLANs       []VLANInfo        `json:"vlans,omitempty"`
	Routes      []RouteEntry      `json:"routes,omitempty"`
	Sources     []string          `json:"sources,omitempty"` // which collectors contributed
	Errors      []string          `json:"errors,omitempty"`
}

// EnrichRequest is the POST body for the enrich endpoint.
type EnrichRequest struct {
	// NETCONF settings (optional — skip NETCONF if empty)
	Netconf *NetconfConfig `json:"netconf,omitempty"`
	// SNMP settings (optional — skip SNMP if empty)
	SNMP *SNMPConfig `json:"snmp,omitempty"`
	// SSH credentials for NETCONF transport
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}
