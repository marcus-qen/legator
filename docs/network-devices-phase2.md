# Network Devices Phase 2: NETCONF + SNMP Inventory Enrichment

## Overview

Phase 2 extends Legator's network device probe with two additional collection protocols:

| Protocol | Transport | Standard | Use case |
|----------|-----------|----------|----------|
| NETCONF | SSH | RFC 6241 / RFC 6242 | Structured config + operational state via YANG models |
| SNMP | UDP | RFC 3416 (v2c), RFC 3414 (v3) | Interface counters, system info, MIB data |

Both sources are **best-effort**: failures are recorded in `errors` and do not abort enrichment. You can use either or both in a single request.

---

## API

### Trigger enrichment

```
POST /api/v1/network-devices/{id}/enrich
POST /api/v1/network/devices/{id}/enrich
```

**Request body:**

```json
{
  "netconf": {
    "host": "10.0.0.1",
    "port": 830,
    "username": "admin",
    "password": "secret"
  },
  "snmp": {
    "host": "10.0.0.1",
    "port": 161,
    "version": 2,
    "community": "public"
  }
}
```

At least one of `netconf` or `snmp` must be present.

**Response:**

```json
{
  "inventory": {
    "device_id": "abc-123",
    "collected_at": "2026-03-04T12:00:00Z",
    "hostname": "core-router-1",
    "vendor": "cisco",
    "firmware": "Cisco IOS Software, Version 15.1(4)M12a",
    "serial": "SN123456789",
    "sys_descr": "Cisco IOS Software...",
    "sys_location": "DC1 Rack 4",
    "interfaces": [...],
    "vlans": [...],
    "routes": [...],
    "sources": ["netconf", "snmp"],
    "errors": []
  }
}
```

### Get interface details

```
GET /api/v1/network-devices/{id}/interfaces
GET /api/v1/network/devices/{id}/interfaces
```

Returns the interface list from the most recent enrichment pass.

```json
{
  "device_id": "abc-123",
  "interfaces": [
    {
      "index": 1,
      "name": "GigabitEthernet0/0",
      "description": "WAN uplink",
      "mac_address": "aa:bb:cc:dd:ee:01",
      "speed_mbps": 1000,
      "admin_up": true,
      "oper_up": true,
      "in_octets": 1234567890,
      "out_octets": 987654321,
      "in_errors": 0,
      "out_errors": 0
    }
  ]
}
```

---

## NETCONF

### Protocol details

- **Transport:** SSH, subsystem `netconf`
- **Default port:** 830
- **Framing:** RFC 6242 base framing (`]]>]]>` end-of-message marker)
- **Capabilities:** `urn:ietf:params:netconf:base:1.0`

### Operations

| RPC | Description |
|-----|-------------|
| `get-config` | Retrieves running configuration |
| `get` | Retrieves operational state |
| `close-session` | Graceful session termination |

### YANG model support

The client parses responses using two strategies:

1. **ietf-interfaces** (`urn:ietf:params:xml:ns:yang:ietf-interfaces`) — standard model, supported by most modern NOS
2. **Raw XML fallback** — scans for `<interface><name>` elements in any namespace

Firmware/version is extracted from common version elements: `os-version`, `version`, `software-version`, `firmware-version`.

### Supported devices (NETCONF)

| Vendor | YANG support | Notes |
|--------|-------------|-------|
| Cisco IOS-XE | ietf-interfaces, Cisco-IOS-XE-native | Port 830 |
| Cisco NX-OS | ietf-interfaces | Port 830 |
| Juniper JunOS | ietf-interfaces, junos-* | Port 830 |
| Fortinet FortiOS | Limited | CLI-over-NETCONF |
| Generic Linux (netopeer2) | ietf-interfaces | Full RFC 6241 |

### Credentials

NETCONF uses the same SSH credential model as the existing probe:

```json
{
  "netconf": { "host": "10.0.0.1", "username": "admin" },
  "password": "secret"
}
```

Or with a private key:

```json
{
  "netconf": { "host": "10.0.0.1", "username": "admin" },
  "private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\n..."
}
```

---

## SNMP

### Protocol details

- **Transport:** UDP
- **Default port:** 161
- **Versions:** v2c and v3
- **Library:** [github.com/gosnmp/gosnmp](https://github.com/gosnmp/gosnmp) (pure Go, no CGO)

### MIBs queried

| OID | Description |
|-----|-------------|
| `1.3.6.1.2.1.1.1.0` | sysDescr |
| `1.3.6.1.2.1.1.5.0` | sysName |
| `1.3.6.1.2.1.1.6.0` | sysLocation |
| `1.3.6.1.2.1.2.2.1.2` | ifDescr (table) |
| `1.3.6.1.2.1.2.2.1.5` | ifSpeed |
| `1.3.6.1.2.1.2.2.1.6` | ifPhysAddress |
| `1.3.6.1.2.1.2.2.1.7` | ifAdminStatus |
| `1.3.6.1.2.1.2.2.1.8` | ifOperStatus |
| `1.3.6.1.2.1.2.2.1.10` | ifInOctets |
| `1.3.6.1.2.1.2.2.1.16` | ifOutOctets |
| `1.3.6.1.2.1.2.2.1.14` | ifInErrors |
| `1.3.6.1.2.1.2.2.1.20` | ifOutErrors |

### SNMPv2c configuration

```json
{
  "snmp": {
    "host": "10.0.0.1",
    "port": 161,
    "version": 2,
    "community": "public"
  }
}
```

### SNMPv3 configuration

```json
{
  "snmp": {
    "host": "10.0.0.1",
    "version": 3,
    "username": "legator",
    "auth_protocol": "sha",
    "auth_password": "authSecret123",
    "priv_protocol": "aes",
    "priv_password": "privSecret456"
  }
}
```

Supported auth protocols: `none`, `md5`, `sha`
Supported privacy protocols: `none`, `des`, `aes`

### Firmware extraction from sysDescr

| Vendor | Pattern matched |
|--------|-----------------|
| Cisco | Line containing "Version" in sysDescr |
| Juniper | Part starting with "JUNOS" |
| Fortinet | Line containing "FortiOS" or "version" |
| Generic | First line containing "version" (case-insensitive) |

---

## Enrichment model

### EnrichedInventory fields

| Field | Type | Source | Description |
|-------|------|--------|-------------|
| `device_id` | string | — | Legator device UUID |
| `collected_at` | timestamp | — | When enrichment ran |
| `hostname` | string | SNMP sysName | Device hostname |
| `vendor` | string | Device record | Normalised vendor string |
| `firmware` | string | NETCONF/SNMP | OS/firmware version string |
| `serial` | string | NETCONF | Hardware serial number |
| `sys_descr` | string | SNMP | Full sysDescr string |
| `sys_location` | string | SNMP | sysLocation |
| `interfaces` | array | Both | Per-interface details |
| `vlans` | array | NETCONF | VLAN table |
| `routes` | array | NETCONF | Routing table |
| `sources` | array | — | Which collectors contributed |
| `errors` | array | — | Non-fatal collection errors |

### Interface merging

When both NETCONF and SNMP are configured, results are merged by interface name:

- SNMP wins for: MAC address, speed, admin/oper status, counters
- NETCONF contributes: interface names, descriptions
- New interfaces from either source are appended
- Final list is sorted by name

---

## Implementation files

| File | Description |
|------|-------------|
| `netconf.go` | NETCONF client (SSH transport, XML framing, RFC 6241/6242) |
| `snmp.go` | SNMP client (v2c + v3, IF-MIB walk, gosnmp) |
| `enrichment.go` | Orchestrator — runs NETCONF + SNMP, merges results |
| `store_enrichment.go` | SQLite persistence for enriched inventory |
| `handlers_enrich.go` | HTTP handlers for /enrich and /interfaces |
| `types_ext.go` | New types: NetconfConfig, SNMPConfig, InterfaceDetail, etc. |
| `netconf_test.go` | NETCONF parsing and framing tests |
| `snmp_test.go` | SNMP value parsing and config tests |
| `enrichment_test.go` | Enrichment orchestration and store tests |

---

## Security notes

- NETCONF host key verification is disabled (`InsecureIgnoreHostKey`) for the MVP. Production deployments should populate a known-hosts store and use `ssh.FixedHostKey` or `ssh.KnownHostsFunc`.
- SNMP community strings are transmitted in cleartext for v2c. Use SNMPv3 with AuthPriv for sensitive environments.
- Credentials in EnrichRequest are never stored — they are used only for the duration of the enrichment call.

---

## Example: enrich a Cisco device

```bash
# Register device
curl -X POST http://localhost:8080/api/v1/network/devices \
  -H 'Content-Type: application/json' \
  -d '{"name":"core-router","host":"10.0.0.1","vendor":"cisco","username":"netops","auth_mode":"password"}'

# Trigger enrichment
curl -X POST http://localhost:8080/api/v1/network-devices/DEVICE_ID/enrich \
  -H 'Content-Type: application/json' \
  -d '{
    "password": "secret",
    "netconf": {"host":"10.0.0.1","port":830,"username":"netops"},
    "snmp": {"host":"10.0.0.1","version":2,"community":"public"}
  }'

# Get interface details
curl http://localhost:8080/api/v1/network-devices/DEVICE_ID/interfaces
```
