package networkdevices

// Enrichment orchestrates NETCONF + SNMP data collection and merges the
// results into an EnrichedInventory. Both sources are best-effort: failures
// are recorded in Errors and do not abort the enrichment.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// EnricherOptions controls which sources are queried.
type EnricherOptions struct {
	// NetconfFactory creates a NETCONF client for a given config.
	// Defaults to NewNetconfClient if nil (real SSH connection).
	NetconfFactory func(ctx context.Context, cfg NetconfConfig, creds CredentialInput) (NetconfClientInterface, error)

	// SNMPFactory creates an SNMP client for a given config.
	// Defaults to NewSNMPClient if nil (real UDP connection).
	SNMPFactory func(cfg SNMPConfig) (SNMPClientInterface, error)
}

// Enricher orchestrates NETCONF + SNMP enrichment for a device.
type Enricher struct {
	store *Store
	opts  EnricherOptions
}

// NewEnricher creates an Enricher backed by the given store.
func NewEnricher(store *Store, opts EnricherOptions) *Enricher {
	if opts.NetconfFactory == nil {
		opts.NetconfFactory = func(ctx context.Context, cfg NetconfConfig, creds CredentialInput) (NetconfClientInterface, error) {
			return NewNetconfClient(ctx, cfg, creds)
		}
	}
	if opts.SNMPFactory == nil {
		opts.SNMPFactory = func(cfg SNMPConfig) (SNMPClientInterface, error) {
			return NewSNMPClient(cfg)
		}
	}
	return &Enricher{store: store, opts: opts}
}

// Enrich runs NETCONF and/or SNMP collection for the device, merges results,
// persists them, and returns the enriched inventory.
func (e *Enricher) Enrich(ctx context.Context, device Device, req EnrichRequest) (*EnrichedInventory, error) {
	inv := &EnrichedInventory{
		DeviceID:    device.ID,
		CollectedAt: time.Now().UTC(),
		Vendor:      normalizeVendor(device.Vendor),
	}

	creds := CredentialInput{
		Password:   strings.TrimSpace(req.Password),
		PrivateKey: strings.TrimSpace(req.PrivateKey),
	}

	// --- NETCONF collection ---
	if req.Netconf != nil {
		cfg := *req.Netconf
		if cfg.Host == "" {
			cfg.Host = device.Host
		}
		if cfg.Username == "" {
			cfg.Username = device.Username
		}
		nr := e.collectNetconf(ctx, cfg, creds)
		if nr.Firmware != "" {
			inv.Firmware = nr.Firmware
		}
		if len(nr.Interfaces) > 0 {
			inv.Interfaces = mergeInterfaces(inv.Interfaces, nr.Interfaces)
		}
		for _, errMsg := range nr.Errors {
			inv.Errors = append(inv.Errors, "netconf: "+errMsg)
		}
		inv.Sources = append(inv.Sources, "netconf")
	}

	// --- SNMP collection ---
	if req.SNMP != nil {
		cfg := *req.SNMP
		if cfg.Host == "" {
			cfg.Host = device.Host
		}
		sr := e.collectSNMP(cfg, device.Vendor)
		if inv.Hostname == "" && sr.SysName != "" {
			inv.Hostname = sr.SysName
		}
		if sr.SysDescr != "" {
			inv.SysDescr = sr.SysDescr
		}
		if sr.SysLocation != "" {
			inv.SysLocation = sr.SysLocation
		}
		if inv.Firmware == "" && sr.Firmware != "" {
			inv.Firmware = sr.Firmware
		}
		if len(sr.Interfaces) > 0 {
			inv.Interfaces = mergeInterfaces(inv.Interfaces, sr.Interfaces)
		}
		for _, errMsg := range sr.Errors {
			inv.Errors = append(inv.Errors, "snmp: "+errMsg)
		}
		inv.Sources = append(inv.Sources, "snmp")
	}

	// Persist to store
	if e.store != nil {
		if saveErr := e.store.SaveEnrichedInventory(*inv); saveErr != nil {
			inv.Errors = append(inv.Errors, fmt.Sprintf("storage: %s", saveErr.Error()))
		}
	}

	if len(inv.Errors) == 0 {
		inv.Errors = nil
	}
	return inv, nil
}

// collectNetconf runs NETCONF collection and returns a partial result.
func (e *Enricher) collectNetconf(ctx context.Context, cfg NetconfConfig, creds CredentialInput) NetconfResult {
	var result NetconfResult

	client, err := e.opts.NetconfFactory(ctx, cfg, creds)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("connect: %s", err.Error()))
		return result
	}
	defer func() { _ = client.Close() }()

	// Get running config (best-effort)
	configData, err := client.GetConfig(ctx, "")
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("get-config: %s", err.Error()))
	} else {
		if ifaces := ParseNetconfInterfaces(configData); len(ifaces) > 0 {
			result.Interfaces = ifaces
		}
		if fw := ParseNetconfFirmware(configData); fw != "" {
			result.Firmware = fw
		}
	}

	// Get operational state (best-effort)
	stateData, err := client.Get(ctx, "")
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("get: %s", err.Error()))
	} else if len(result.Interfaces) == 0 {
		if ifaces := ParseNetconfInterfaces(stateData); len(ifaces) > 0 {
			result.Interfaces = ifaces
		}
		if result.Firmware == "" {
			if fw := ParseNetconfFirmware(stateData); fw != "" {
				result.Firmware = fw
			}
		}
	}

	return result
}

// collectSNMP runs SNMP collection and returns a partial result.
func (e *Enricher) collectSNMP(cfg SNMPConfig, vendor string) SNMPResult {
	var result SNMPResult

	client, err := e.opts.SNMPFactory(cfg)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("connect: %s", err.Error()))
		return result
	}
	defer func() { _ = client.Close() }()

	// System info
	sysInfo, err := client.GetSystem()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("get-system: %s", err.Error()))
	} else {
		result.SysDescr = sysInfo.SysDescr
		result.SysName = sysInfo.SysName
		result.SysLocation = sysInfo.SysLocation
		result.Firmware = ParseSNMPFirmware(sysInfo.SysDescr, normalizeVendor(vendor))
	}

	// Interface table
	ifaces, err := client.GetInterfaces()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("get-interfaces: %s", err.Error()))
	} else {
		result.Interfaces = ifaces
	}

	return result
}

// mergeInterfaces combines interface details from two sources.
// Entries from 'overlay' enrich matching entries in 'base' by name;
// entries not present in base are appended.
func mergeInterfaces(base, overlay []InterfaceDetail) []InterfaceDetail {
	if len(base) == 0 {
		return overlay
	}
	index := make(map[string]int, len(base))
	for i, iface := range base {
		index[iface.Name] = i
	}
	for _, iface := range overlay {
		if i, ok := index[iface.Name]; ok {
			// Enrich existing entry — overlay wins for non-zero fields
			existing := base[i]
			if iface.MACAddress != "" {
				existing.MACAddress = iface.MACAddress
			}
			if iface.SpeedMbps > 0 {
				existing.SpeedMbps = iface.SpeedMbps
			}
			if iface.Description != "" {
				existing.Description = iface.Description
			}
			if iface.InOctets > 0 {
				existing.InOctets = iface.InOctets
			}
			if iface.OutOctets > 0 {
				existing.OutOctets = iface.OutOctets
			}
			if iface.InErrors > 0 {
				existing.InErrors = iface.InErrors
			}
			if iface.OutErrors > 0 {
				existing.OutErrors = iface.OutErrors
			}
			// Status: prefer SNMP operational status
			existing.AdminUp = iface.AdminUp
			existing.OperUp = iface.OperUp
			base[i] = existing
		} else {
			base = append(base, iface)
			index[iface.Name] = len(base) - 1
		}
	}
	// Sort by name for deterministic output
	sort.Slice(base, func(i, j int) bool {
		return base[i].Name < base[j].Name
	})
	return base
}
