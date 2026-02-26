package alerts

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Store persists alert rules and alert history in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) an alerts database.
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open alerts db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS alert_rules (
		id             TEXT PRIMARY KEY,
		name           TEXT NOT NULL,
		description    TEXT NOT NULL DEFAULT '',
		enabled        INTEGER NOT NULL DEFAULT 1,
		condition_json TEXT NOT NULL,
		actions_json   TEXT NOT NULL,
		created_at     TEXT NOT NULL,
		updated_at     TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create alert_rules: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS alert_events (
		id          TEXT PRIMARY KEY,
		rule_id     TEXT NOT NULL,
		rule_name   TEXT NOT NULL,
		probe_id    TEXT NOT NULL,
		status      TEXT NOT NULL,
		message     TEXT NOT NULL,
		fired_at    TEXT NOT NULL,
		resolved_at TEXT
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create alert_events: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_alert_rules_updated_at ON alert_rules(updated_at)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_alert_events_rule_id ON alert_events(rule_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_alert_events_status ON alert_events(status)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_alert_events_fired_at ON alert_events(fired_at)`)

	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// CreateRule inserts a new alert rule.
func (s *Store) CreateRule(rule AlertRule) (*AlertRule, error) {
	now := time.Now().UTC()
	if rule.ID == "" {
		rule.ID = uuid.NewString()
	}
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = now
	}
	rule.UpdatedAt = now

	conditionJSON, err := json.Marshal(rule.Condition)
	if err != nil {
		return nil, fmt.Errorf("marshal condition: %w", err)
	}
	actionsJSON, err := json.Marshal(rule.Actions)
	if err != nil {
		return nil, fmt.Errorf("marshal actions: %w", err)
	}

	enabled := 0
	if rule.Enabled {
		enabled = 1
	}

	_, err = s.db.Exec(`INSERT INTO alert_rules (id, name, description, enabled, condition_json, actions_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		rule.ID,
		rule.Name,
		rule.Description,
		enabled,
		string(conditionJSON),
		string(actionsJSON),
		rule.CreatedAt.Format(time.RFC3339Nano),
		rule.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("insert alert rule: %w", err)
	}

	copyRule := rule
	return &copyRule, nil
}

// UpdateRule updates an existing alert rule.
func (s *Store) UpdateRule(rule AlertRule) (*AlertRule, error) {
	if rule.ID == "" {
		return nil, fmt.Errorf("rule id required")
	}

	now := time.Now().UTC()
	rule.UpdatedAt = now

	conditionJSON, err := json.Marshal(rule.Condition)
	if err != nil {
		return nil, fmt.Errorf("marshal condition: %w", err)
	}
	actionsJSON, err := json.Marshal(rule.Actions)
	if err != nil {
		return nil, fmt.Errorf("marshal actions: %w", err)
	}

	enabled := 0
	if rule.Enabled {
		enabled = 1
	}

	result, err := s.db.Exec(`UPDATE alert_rules
		SET name = ?, description = ?, enabled = ?, condition_json = ?, actions_json = ?, updated_at = ?
		WHERE id = ?`,
		rule.Name,
		rule.Description,
		enabled,
		string(conditionJSON),
		string(actionsJSON),
		rule.UpdatedAt.Format(time.RFC3339Nano),
		rule.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update alert rule: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, sql.ErrNoRows
	}

	updated, err := s.GetRule(rule.ID)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// GetRule returns one alert rule by ID.
func (s *Store) GetRule(id string) (*AlertRule, error) {
	row := s.db.QueryRow(`SELECT id, name, description, enabled, condition_json, actions_json, created_at, updated_at
		FROM alert_rules WHERE id = ?`, id)

	rule, err := scanRule(row)
	if err != nil {
		return nil, err
	}
	return rule, nil
}

// ListRules returns all alert rules (newest first).
func (s *Store) ListRules() ([]AlertRule, error) {
	rows, err := s.db.Query(`SELECT id, name, description, enabled, condition_json, actions_json, created_at, updated_at
		FROM alert_rules
		ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]AlertRule, 0)
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			continue
		}
		out = append(out, *rule)
	}
	return out, rows.Err()
}

// DeleteRule deletes a rule by ID.
func (s *Store) DeleteRule(id string) error {
	result, err := s.db.Exec(`DELETE FROM alert_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RecordEvent inserts or updates an alert event.
func (s *Store) RecordEvent(event AlertEvent) error {
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.FiredAt.IsZero() {
		event.FiredAt = time.Now().UTC()
	}
	if event.Status == "resolved" && event.ResolvedAt == nil {
		now := time.Now().UTC()
		event.ResolvedAt = &now
	}

	var resolvedAt sql.NullString
	if event.ResolvedAt != nil {
		resolvedAt = sql.NullString{String: event.ResolvedAt.UTC().Format(time.RFC3339Nano), Valid: true}
	}

	_, err := s.db.Exec(`INSERT INTO alert_events (id, rule_id, rule_name, probe_id, status, message, fired_at, resolved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			rule_id = excluded.rule_id,
			rule_name = excluded.rule_name,
			probe_id = excluded.probe_id,
			status = excluded.status,
			message = excluded.message,
			fired_at = excluded.fired_at,
			resolved_at = excluded.resolved_at`,
		event.ID,
		event.RuleID,
		event.RuleName,
		event.ProbeID,
		event.Status,
		event.Message,
		event.FiredAt.UTC().Format(time.RFC3339Nano),
		resolvedAt,
	)
	return err
}

// ListEvents returns recent events for one rule (or all rules when ruleID empty).
func (s *Store) ListEvents(ruleID string, limit int) []AlertEvent {
	if limit <= 0 {
		limit = 100
	}

	query := `SELECT id, rule_id, rule_name, probe_id, status, message, fired_at, resolved_at
		FROM alert_events`
	args := make([]any, 0, 2)
	if ruleID != "" {
		query += ` WHERE rule_id = ?`
		args = append(args, ruleID)
	}
	query += ` ORDER BY fired_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make([]AlertEvent, 0, limit)
	for rows.Next() {
		evt, err := scanEvent(rows)
		if err != nil {
			continue
		}
		out = append(out, *evt)
	}
	return out
}

// ActiveAlerts returns all currently firing alerts.
func (s *Store) ActiveAlerts() []AlertEvent {
	rows, err := s.db.Query(`SELECT id, rule_id, rule_name, probe_id, status, message, fired_at, resolved_at
		FROM alert_events
		WHERE status = 'firing'
		ORDER BY fired_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := make([]AlertEvent, 0)
	for rows.Next() {
		evt, err := scanEvent(rows)
		if err != nil {
			continue
		}
		out = append(out, *evt)
	}
	return out
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRule(s scanner) (*AlertRule, error) {
	var (
		rule                       AlertRule
		enabled                    int
		conditionJSON, actionsJSON string
		createdAt, updatedAt       string
	)

	if err := s.Scan(
		&rule.ID,
		&rule.Name,
		&rule.Description,
		&enabled,
		&conditionJSON,
		&actionsJSON,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}

	rule.Enabled = enabled == 1
	_ = json.Unmarshal([]byte(conditionJSON), &rule.Condition)
	_ = json.Unmarshal([]byte(actionsJSON), &rule.Actions)
	rule.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	rule.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)

	return &rule, nil
}

func scanEvent(s scanner) (*AlertEvent, error) {
	var (
		event      AlertEvent
		firedAt    string
		resolvedAt sql.NullString
	)

	if err := s.Scan(
		&event.ID,
		&event.RuleID,
		&event.RuleName,
		&event.ProbeID,
		&event.Status,
		&event.Message,
		&firedAt,
		&resolvedAt,
	); err != nil {
		return nil, err
	}

	event.FiredAt, _ = time.Parse(time.RFC3339Nano, firedAt)
	if resolvedAt.Valid && resolvedAt.String != "" {
		ts, err := time.Parse(time.RFC3339Nano, resolvedAt.String)
		if err == nil {
			event.ResolvedAt = &ts
		}
	}

	return &event, nil
}

// IsNotFound reports whether err is sql.ErrNoRows.
func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
