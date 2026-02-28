package cloudconnectors

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Store persists cloud connectors and normalized assets.
type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open cloud connector db: %w", err)
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

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS cloud_connectors (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		provider    TEXT NOT NULL,
		auth_mode   TEXT NOT NULL,
		is_enabled  INTEGER NOT NULL DEFAULT 1,
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL,
		last_scan_at TEXT,
		last_status TEXT,
		last_error  TEXT
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create cloud_connectors: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS cloud_assets (
		id            TEXT PRIMARY KEY,
		connector_id  TEXT NOT NULL,
		provider      TEXT NOT NULL,
		scope_id      TEXT,
		region        TEXT,
		asset_type    TEXT NOT NULL,
		asset_id      TEXT NOT NULL,
		display_name  TEXT,
		status        TEXT,
		raw_json      TEXT NOT NULL,
		discovered_at TEXT NOT NULL,
		FOREIGN KEY(connector_id) REFERENCES cloud_connectors(id) ON DELETE CASCADE
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create cloud_assets: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_cloud_connectors_provider ON cloud_connectors(provider)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_cloud_connectors_updated ON cloud_connectors(updated_at)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_cloud_assets_connector ON cloud_assets(connector_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_cloud_assets_provider ON cloud_assets(provider)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_cloud_assets_discovered ON cloud_assets(discovered_at DESC)`)

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) ListConnectors() ([]Connector, error) {
	rows, err := s.db.Query(`SELECT
		id, name, provider, auth_mode, is_enabled, created_at, updated_at, last_scan_at, last_status, last_error
		FROM cloud_connectors
		ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Connector, 0)
	for rows.Next() {
		connector, err := scanConnector(rows)
		if err != nil {
			continue
		}
		out = append(out, *connector)
	}

	return out, rows.Err()
}

func (s *Store) GetConnector(id string) (*Connector, error) {
	row := s.db.QueryRow(`SELECT
		id, name, provider, auth_mode, is_enabled, created_at, updated_at, last_scan_at, last_status, last_error
		FROM cloud_connectors
		WHERE id = ?`, id)
	return scanConnector(row)
}

func (s *Store) CreateConnector(connector Connector) (*Connector, error) {
	now := time.Now().UTC()
	if connector.ID == "" {
		connector.ID = uuid.NewString()
	}

	connector.Name = strings.TrimSpace(connector.Name)
	connector.Provider = normalizeProvider(connector.Provider)
	connector.AuthMode = normalizeAuthMode(connector.AuthMode)
	connector.CreatedAt = now
	connector.UpdatedAt = now

	enabled := 0
	if connector.IsEnabled {
		enabled = 1
	}

	if _, err := s.db.Exec(`INSERT INTO cloud_connectors
		(id, name, provider, auth_mode, is_enabled, created_at, updated_at, last_scan_at, last_status, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		connector.ID,
		connector.Name,
		connector.Provider,
		connector.AuthMode,
		enabled,
		connector.CreatedAt.Format(time.RFC3339Nano),
		connector.UpdatedAt.Format(time.RFC3339Nano),
		nullTime(connector.LastScanAt),
		nullString(connector.LastStatus),
		nullString(connector.LastError),
	); err != nil {
		return nil, fmt.Errorf("insert connector: %w", err)
	}

	return s.GetConnector(connector.ID)
}

func (s *Store) UpdateConnector(id string, connector Connector) (*Connector, error) {
	existing, err := s.GetConnector(id)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	name := strings.TrimSpace(connector.Name)
	if name == "" {
		name = existing.Name
	}
	provider := normalizeProvider(connector.Provider)
	if provider == "" {
		provider = existing.Provider
	}
	authMode := normalizeAuthMode(connector.AuthMode)
	if authMode == "" {
		authMode = existing.AuthMode
	}
	isEnabled := connector.IsEnabled

	result, err := s.db.Exec(`UPDATE cloud_connectors
		SET name = ?, provider = ?, auth_mode = ?, is_enabled = ?, updated_at = ?
		WHERE id = ?`,
		name,
		provider,
		enableAuthMode(authMode),
		boolToInt(isEnabled),
		now.Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("update connector: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, sql.ErrNoRows
	}

	return s.GetConnector(id)
}

func (s *Store) DeleteConnector(id string) error {
	result, err := s.db.Exec(`DELETE FROM cloud_connectors WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SetConnectorScanResult(connectorID string, status, scanErr string, scannedAt time.Time) error {
	if scannedAt.IsZero() {
		scannedAt = time.Now().UTC()
	}
	updatedAt := time.Now().UTC()
	result, err := s.db.Exec(`UPDATE cloud_connectors
		SET last_scan_at = ?, last_status = ?, last_error = ?, updated_at = ?
		WHERE id = ?`,
		scannedAt.Format(time.RFC3339Nano),
		strings.TrimSpace(strings.ToLower(status)),
		nullString(scanErr),
		updatedAt.Format(time.RFC3339Nano),
		connectorID,
	)
	if err != nil {
		return fmt.Errorf("set scan result: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ReplaceAssetsForConnector(connector Connector, assets []Asset) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM cloud_assets WHERE connector_id = ?`, connector.ID); err != nil {
		return fmt.Errorf("delete old assets: %w", err)
	}

	now := time.Now().UTC()
	for _, asset := range assets {
		if strings.TrimSpace(asset.ID) == "" {
			asset.ID = uuid.NewString()
		}
		if asset.DiscoveredAt.IsZero() {
			asset.DiscoveredAt = now
		}

		if strings.TrimSpace(asset.ConnectorID) == "" {
			asset.ConnectorID = connector.ID
		}
		if strings.TrimSpace(asset.Provider) == "" {
			asset.Provider = connector.Provider
		}

		if _, err := tx.Exec(`INSERT INTO cloud_assets
			(id, connector_id, provider, scope_id, region, asset_type, asset_id, display_name, status, raw_json, discovered_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			asset.ID,
			asset.ConnectorID,
			normalizeProvider(asset.Provider),
			strings.TrimSpace(asset.ScopeID),
			strings.TrimSpace(asset.Region),
			strings.TrimSpace(asset.AssetType),
			strings.TrimSpace(asset.AssetID),
			strings.TrimSpace(asset.DisplayName),
			strings.TrimSpace(asset.Status),
			normalizeRawJSON(asset.RawJSON),
			asset.DiscoveredAt.UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("insert asset: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) ListAssets(filter AssetFilter) ([]Asset, error) {
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 3)

	if p := normalizeProvider(filter.Provider); p != "" {
		clauses = append(clauses, "provider = ?")
		args = append(args, p)
	}
	if id := strings.TrimSpace(filter.ConnectorID); id != "" {
		clauses = append(clauses, "connector_id = ?")
		args = append(args, id)
	}

	query := `SELECT
		id, connector_id, provider, scope_id, region, asset_type, asset_id, display_name, status, raw_json, discovered_at
		FROM cloud_assets`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY discovered_at DESC"

	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	query += " LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Asset, 0)
	for rows.Next() {
		asset, err := scanAsset(rows)
		if err != nil {
			continue
		}
		out = append(out, *asset)
	}

	return out, rows.Err()
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func normalizeAuthMode(authMode string) string {
	value := strings.ToLower(strings.TrimSpace(authMode))
	if value == "" {
		return AuthModeCLI
	}
	return value
}

func enableAuthMode(authMode string) string {
	if strings.TrimSpace(authMode) == "" {
		return AuthModeCLI
	}
	return authMode
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullString(v string) any {
	value := strings.TrimSpace(v)
	if value == "" {
		return nil
	}
	return value
}

func nullTime(v time.Time) any {
	if v.IsZero() {
		return nil
	}
	return v.UTC().Format(time.RFC3339Nano)
}

func normalizeRawJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	return raw
}

type scanner interface {
	Scan(dest ...any) error
}

func scanConnector(row scanner) (*Connector, error) {
	var (
		connector                        Connector
		enabled                          int
		createdAtRaw, updatedAtRaw       string
		lastScanRaw, lastStatus, lastErr sql.NullString
	)

	if err := row.Scan(
		&connector.ID,
		&connector.Name,
		&connector.Provider,
		&connector.AuthMode,
		&enabled,
		&createdAtRaw,
		&updatedAtRaw,
		&lastScanRaw,
		&lastStatus,
		&lastErr,
	); err != nil {
		return nil, err
	}

	connector.IsEnabled = enabled == 1
	connector.Provider = normalizeProvider(connector.Provider)
	connector.AuthMode = normalizeAuthMode(connector.AuthMode)
	connector.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtRaw)
	connector.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAtRaw)
	if lastScanRaw.Valid {
		connector.LastScanAt, _ = time.Parse(time.RFC3339Nano, lastScanRaw.String)
	}
	if lastStatus.Valid {
		connector.LastStatus = lastStatus.String
	}
	if lastErr.Valid {
		connector.LastError = lastErr.String
	}

	return &connector, nil
}

func scanAsset(row scanner) (*Asset, error) {
	var (
		asset           Asset
		discoveredAtRaw string
	)

	if err := row.Scan(
		&asset.ID,
		&asset.ConnectorID,
		&asset.Provider,
		&asset.ScopeID,
		&asset.Region,
		&asset.AssetType,
		&asset.AssetID,
		&asset.DisplayName,
		&asset.Status,
		&asset.RawJSON,
		&discoveredAtRaw,
	); err != nil {
		return nil, err
	}

	asset.Provider = normalizeProvider(asset.Provider)
	asset.DiscoveredAt, _ = time.Parse(time.RFC3339Nano, discoveredAtRaw)
	return &asset, nil
}

func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
