package networkdevices

// Store extensions for enriched inventory persistence.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ensureEnrichedTable creates the enriched_inventory table if it doesn't exist.
func (s *Store) ensureEnrichedTable() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS network_device_enriched (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		device_id    TEXT NOT NULL,
		collected_at TEXT NOT NULL,
		hostname     TEXT NOT NULL DEFAULT '',
		vendor       TEXT NOT NULL DEFAULT '',
		firmware     TEXT NOT NULL DEFAULT '',
		serial       TEXT NOT NULL DEFAULT '',
		sys_descr    TEXT NOT NULL DEFAULT '',
		sys_location TEXT NOT NULL DEFAULT '',
		interfaces   TEXT NOT NULL DEFAULT '[]',
		vlans        TEXT NOT NULL DEFAULT '[]',
		routes       TEXT NOT NULL DEFAULT '[]',
		sources      TEXT NOT NULL DEFAULT '[]',
		errors       TEXT NOT NULL DEFAULT '[]'
	)`)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_nd_enriched_device
		ON network_device_enriched(device_id, collected_at DESC)`)
	return nil
}

// SaveEnrichedInventory persists an enriched inventory result.
func (s *Store) SaveEnrichedInventory(inv EnrichedInventory) error {
	if err := s.ensureEnrichedTable(); err != nil {
		return fmt.Errorf("ensure enriched table: %w", err)
	}

	ifacesJSON, _ := json.Marshal(inv.Interfaces)
	vlansJSON, _ := json.Marshal(inv.VLANs)
	routesJSON, _ := json.Marshal(inv.Routes)
	sourcesJSON, _ := json.Marshal(inv.Sources)
	errorsJSON, _ := json.Marshal(inv.Errors)

	collectedAt := inv.CollectedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}

	_, err := s.db.Exec(`INSERT INTO network_device_enriched
		(device_id, collected_at, hostname, vendor, firmware, serial,
		 sys_descr, sys_location, interfaces, vlans, routes, sources, errors)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(inv.DeviceID),
		collectedAt.Format(time.RFC3339Nano),
		inv.Hostname,
		inv.Vendor,
		inv.Firmware,
		inv.Serial,
		inv.SysDescr,
		inv.SysLocation,
		string(ifacesJSON),
		string(vlansJSON),
		string(routesJSON),
		string(sourcesJSON),
		string(errorsJSON),
	)
	return err
}

// GetEnrichedInventory retrieves the most recent enriched inventory for a device.
func (s *Store) GetEnrichedInventory(deviceID string) (*EnrichedInventory, error) {
	if err := s.ensureEnrichedTable(); err != nil {
		return nil, fmt.Errorf("ensure enriched table: %w", err)
	}

	var inv EnrichedInventory
	var collectedAt, ifacesRaw, vlansRaw, routesRaw, sourcesRaw, errorsRaw string

	err := s.db.QueryRow(`SELECT
		device_id, collected_at, hostname, vendor, firmware, serial,
		sys_descr, sys_location, interfaces, vlans, routes, sources, errors
		FROM network_device_enriched
		WHERE device_id = ?
		ORDER BY collected_at DESC
		LIMIT 1`,
		strings.TrimSpace(deviceID),
	).Scan(
		&inv.DeviceID,
		&collectedAt,
		&inv.Hostname,
		&inv.Vendor,
		&inv.Firmware,
		&inv.Serial,
		&inv.SysDescr,
		&inv.SysLocation,
		&ifacesRaw,
		&vlansRaw,
		&routesRaw,
		&sourcesRaw,
		&errorsRaw,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}

	inv.CollectedAt, _ = time.Parse(time.RFC3339Nano, collectedAt)
	_ = json.Unmarshal([]byte(ifacesRaw), &inv.Interfaces)
	_ = json.Unmarshal([]byte(vlansRaw), &inv.VLANs)
	_ = json.Unmarshal([]byte(routesRaw), &inv.Routes)
	_ = json.Unmarshal([]byte(sourcesRaw), &inv.Sources)
	_ = json.Unmarshal([]byte(errorsRaw), &inv.Errors)
	return &inv, nil
}

// GetInterfaceDetails returns the interface list from the latest enriched inventory.
func (s *Store) GetInterfaceDetails(deviceID string) ([]InterfaceDetail, error) {
	inv, err := s.GetEnrichedInventory(deviceID)
	if err != nil {
		return nil, err
	}
	return inv.Interfaces, nil
}
