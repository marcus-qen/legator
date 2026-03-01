package networkdevices

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
	"github.com/marcus-qen/legator/internal/controlplane/migration"
)

// Store persists network device targets in SQLite.
type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open network devices db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS network_devices (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		host        TEXT NOT NULL,
		port        INTEGER NOT NULL,
		vendor      TEXT NOT NULL,
		username    TEXT NOT NULL,
		auth_mode   TEXT NOT NULL,
		tags_json   TEXT NOT NULL DEFAULT '[]',
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create network_devices: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_network_devices_name ON network_devices(name)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_network_devices_host ON network_devices(host)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_network_devices_updated ON network_devices(updated_at DESC)`)

	if err := migration.EnsureVersion(db, 1); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ensure schema version: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) ListDevices() ([]Device, error) {
	rows, err := s.db.Query(`SELECT
		id, name, host, port, vendor, username, auth_mode, tags_json, created_at, updated_at
		FROM network_devices
		ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Device, 0)
	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			continue
		}
		out = append(out, *device)
	}
	return out, rows.Err()
}

func (s *Store) GetDevice(id string) (*Device, error) {
	row := s.db.QueryRow(`SELECT
		id, name, host, port, vendor, username, auth_mode, tags_json, created_at, updated_at
		FROM network_devices
		WHERE id = ?`, strings.TrimSpace(id))
	return scanDevice(row)
}

func (s *Store) CreateDevice(device Device) (*Device, error) {
	now := time.Now().UTC()
	if strings.TrimSpace(device.ID) == "" {
		device.ID = uuid.NewString()
	}

	device.Name = strings.TrimSpace(device.Name)
	device.Host = strings.TrimSpace(device.Host)
	device.Port = normalizePort(device.Port)
	device.Vendor = normalizeVendor(device.Vendor)
	device.Username = strings.TrimSpace(device.Username)
	device.AuthMode = normalizeAuthMode(device.AuthMode)
	device.Tags = normalizeTags(device.Tags)
	device.CreatedAt = now
	device.UpdatedAt = now

	tagsJSON, err := json.Marshal(device.Tags)
	if err != nil {
		return nil, fmt.Errorf("marshal tags: %w", err)
	}

	if _, err := s.db.Exec(`INSERT INTO network_devices
		(id, name, host, port, vendor, username, auth_mode, tags_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		device.ID,
		device.Name,
		device.Host,
		device.Port,
		device.Vendor,
		device.Username,
		device.AuthMode,
		string(tagsJSON),
		device.CreatedAt.Format(time.RFC3339Nano),
		device.UpdatedAt.Format(time.RFC3339Nano),
	); err != nil {
		return nil, fmt.Errorf("insert network device: %w", err)
	}

	return s.GetDevice(device.ID)
}

func (s *Store) UpdateDevice(id string, update Device) (*Device, error) {
	existing, err := s.GetDevice(id)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(update.Name)
	if name == "" {
		name = existing.Name
	}
	host := strings.TrimSpace(update.Host)
	if host == "" {
		host = existing.Host
	}
	port := update.Port
	if port <= 0 {
		port = existing.Port
	}
	vendor := strings.ToLower(strings.TrimSpace(update.Vendor))
	if vendor == "" {
		vendor = existing.Vendor
	} else {
		vendor = normalizeVendor(vendor)
	}
	username := strings.TrimSpace(update.Username)
	if username == "" {
		username = existing.Username
	}
	authMode := strings.ToLower(strings.TrimSpace(update.AuthMode))
	if authMode == "" {
		authMode = existing.AuthMode
	} else {
		authMode = normalizeAuthMode(authMode)
	}
	tags := update.Tags
	if tags == nil {
		tags = existing.Tags
	}
	tags = normalizeTags(tags)

	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return nil, fmt.Errorf("marshal tags: %w", err)
	}

	now := time.Now().UTC()
	result, err := s.db.Exec(`UPDATE network_devices
		SET name = ?, host = ?, port = ?, vendor = ?, username = ?, auth_mode = ?, tags_json = ?, updated_at = ?
		WHERE id = ?`,
		name,
		host,
		normalizePort(port),
		vendor,
		username,
		authMode,
		string(tagsJSON),
		now.Format(time.RFC3339Nano),
		strings.TrimSpace(id),
	)
	if err != nil {
		return nil, fmt.Errorf("update network device: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, sql.ErrNoRows
	}

	return s.GetDevice(id)
}

func (s *Store) DeleteDevice(id string) error {
	result, err := s.db.Exec(`DELETE FROM network_devices WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func normalizePort(port int) int {
	if port <= 0 {
		return 22
	}
	if port > 65535 {
		return 65535
	}
	return port
}

func normalizeVendor(vendor string) string {
	value := strings.ToLower(strings.TrimSpace(vendor))
	if value == "" {
		return VendorGeneric
	}
	return value
}

func normalizeAuthMode(mode string) string {
	value := strings.ToLower(strings.TrimSpace(mode))
	if value == "" {
		return AuthModePassword
	}
	switch value {
	case AuthModePassword, AuthModeAgent, AuthModeKey:
		return value
	default:
		return value
	}
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		normalized := strings.ToLower(strings.TrimSpace(tag))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out
}

type scanner interface {
	Scan(dest ...any) error
}

func scanDevice(row scanner) (*Device, error) {
	var (
		device  Device
		tagsRaw string
		created string
		updated string
	)
	if err := row.Scan(
		&device.ID,
		&device.Name,
		&device.Host,
		&device.Port,
		&device.Vendor,
		&device.Username,
		&device.AuthMode,
		&tagsRaw,
		&created,
		&updated,
	); err != nil {
		return nil, err
	}

	device.Vendor = normalizeVendor(device.Vendor)
	device.AuthMode = normalizeAuthMode(device.AuthMode)
	device.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	device.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	_ = json.Unmarshal([]byte(tagsRaw), &device.Tags)
	device.Tags = normalizeTags(device.Tags)

	return &device, nil
}

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
