package alerts

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// RoutingStore persists routing and escalation policies in SQLite.
type RoutingStore struct {
	db *sql.DB
}

// NewRoutingStore opens (or creates) a routing policy database at dbPath.
func NewRoutingStore(dbPath string) (*RoutingStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open routing db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS routing_policies (
		id                   TEXT PRIMARY KEY,
		name                 TEXT NOT NULL,
		description          TEXT NOT NULL DEFAULT '',
		priority             INTEGER NOT NULL DEFAULT 0,
		is_default           INTEGER NOT NULL DEFAULT 0,
		matchers_json        TEXT NOT NULL DEFAULT '[]',
		owner_label          TEXT NOT NULL DEFAULT '',
		owner_contact        TEXT NOT NULL DEFAULT '',
		escalation_policy_id TEXT NOT NULL DEFAULT '',
		runbook_url          TEXT NOT NULL DEFAULT '',
		created_at           TEXT NOT NULL,
		updated_at           TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create routing_policies: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS escalation_policies (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		steps_json  TEXT NOT NULL DEFAULT '[]',
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create escalation_policies: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_routing_policies_priority ON routing_policies(priority DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_routing_policies_updated_at ON routing_policies(updated_at)`)

	return &RoutingStore{db: db}, nil
}

// Close closes the underlying database.
func (rs *RoutingStore) Close() error {
	if rs == nil || rs.db == nil {
		return nil
	}
	return rs.db.Close()
}

// -------------------------------------------------------------------
// RoutingPolicy CRUD
// -------------------------------------------------------------------

// CreateRoutingPolicy inserts a new routing policy.
func (rs *RoutingStore) CreateRoutingPolicy(p RoutingPolicy) (*RoutingPolicy, error) {
	now := time.Now().UTC()
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	p.CreatedAt = now
	p.UpdatedAt = now

	matchersJSON, err := json.Marshal(p.Matchers)
	if err != nil {
		return nil, fmt.Errorf("marshal matchers: %w", err)
	}

	isDefault := 0
	if p.IsDefault {
		isDefault = 1
	}

	_, err = rs.db.Exec(`INSERT INTO routing_policies
		(id, name, description, priority, is_default, matchers_json, owner_label, owner_contact, escalation_policy_id, runbook_url, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Description, p.Priority, isDefault,
		string(matchersJSON), p.OwnerLabel, p.OwnerContact,
		p.EscalationPolicyID, p.RunbookURL,
		p.CreatedAt.Format(time.RFC3339Nano),
		p.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("insert routing policy: %w", err)
	}
	cp := p
	return &cp, nil
}

// UpdateRoutingPolicy updates an existing routing policy.
func (rs *RoutingStore) UpdateRoutingPolicy(p RoutingPolicy) (*RoutingPolicy, error) {
	if p.ID == "" {
		return nil, fmt.Errorf("routing policy id required")
	}
	// Preserve CreatedAt
	existing, err := rs.GetRoutingPolicy(p.ID)
	if err != nil {
		return nil, err
	}
	p.CreatedAt = existing.CreatedAt
	p.UpdatedAt = time.Now().UTC()

	matchersJSON, err := json.Marshal(p.Matchers)
	if err != nil {
		return nil, fmt.Errorf("marshal matchers: %w", err)
	}

	isDefault := 0
	if p.IsDefault {
		isDefault = 1
	}

	result, err := rs.db.Exec(`UPDATE routing_policies
		SET name=?, description=?, priority=?, is_default=?, matchers_json=?,
		    owner_label=?, owner_contact=?, escalation_policy_id=?, runbook_url=?, updated_at=?
		WHERE id=?`,
		p.Name, p.Description, p.Priority, isDefault,
		string(matchersJSON), p.OwnerLabel, p.OwnerContact,
		p.EscalationPolicyID, p.RunbookURL,
		p.UpdatedAt.Format(time.RFC3339Nano),
		p.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update routing policy: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, sql.ErrNoRows
	}
	cp := p
	return &cp, nil
}

// GetRoutingPolicy returns one routing policy by ID.
func (rs *RoutingStore) GetRoutingPolicy(id string) (*RoutingPolicy, error) {
	row := rs.db.QueryRow(`SELECT id, name, description, priority, is_default, matchers_json,
		owner_label, owner_contact, escalation_policy_id, runbook_url, created_at, updated_at
		FROM routing_policies WHERE id=?`, id)
	return scanRoutingPolicy(row)
}

// ListRoutingPolicies returns all routing policies ordered by priority desc then updated_at desc.
func (rs *RoutingStore) ListRoutingPolicies() ([]RoutingPolicy, error) {
	rows, err := rs.db.Query(`SELECT id, name, description, priority, is_default, matchers_json,
		owner_label, owner_contact, escalation_policy_id, runbook_url, created_at, updated_at
		FROM routing_policies ORDER BY priority DESC, updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RoutingPolicy, 0)
	for rows.Next() {
		p, err := scanRoutingPolicy(rows)
		if err != nil {
			continue
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// DeleteRoutingPolicy deletes a routing policy by ID.
func (rs *RoutingStore) DeleteRoutingPolicy(id string) error {
	result, err := rs.db.Exec(`DELETE FROM routing_policies WHERE id=?`, id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// -------------------------------------------------------------------
// EscalationPolicy CRUD
// -------------------------------------------------------------------

// CreateEscalationPolicy inserts a new escalation policy.
func (rs *RoutingStore) CreateEscalationPolicy(ep EscalationPolicy) (*EscalationPolicy, error) {
	now := time.Now().UTC()
	if ep.ID == "" {
		ep.ID = uuid.NewString()
	}
	ep.CreatedAt = now
	ep.UpdatedAt = now

	stepsJSON, err := json.Marshal(ep.Steps)
	if err != nil {
		return nil, fmt.Errorf("marshal steps: %w", err)
	}

	_, err = rs.db.Exec(`INSERT INTO escalation_policies (id, name, description, steps_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		ep.ID, ep.Name, ep.Description, string(stepsJSON),
		ep.CreatedAt.Format(time.RFC3339Nano),
		ep.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("insert escalation policy: %w", err)
	}
	cp := ep
	return &cp, nil
}

// UpdateEscalationPolicy updates an existing escalation policy.
func (rs *RoutingStore) UpdateEscalationPolicy(ep EscalationPolicy) (*EscalationPolicy, error) {
	if ep.ID == "" {
		return nil, fmt.Errorf("escalation policy id required")
	}
	existing, err := rs.GetEscalationPolicy(ep.ID)
	if err != nil {
		return nil, err
	}
	ep.CreatedAt = existing.CreatedAt
	ep.UpdatedAt = time.Now().UTC()

	stepsJSON, err := json.Marshal(ep.Steps)
	if err != nil {
		return nil, fmt.Errorf("marshal steps: %w", err)
	}

	result, err := rs.db.Exec(`UPDATE escalation_policies
		SET name=?, description=?, steps_json=?, updated_at=? WHERE id=?`,
		ep.Name, ep.Description, string(stepsJSON),
		ep.UpdatedAt.Format(time.RFC3339Nano), ep.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update escalation policy: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, sql.ErrNoRows
	}
	cp := ep
	return &cp, nil
}

// GetEscalationPolicy returns one escalation policy by ID.
func (rs *RoutingStore) GetEscalationPolicy(id string) (*EscalationPolicy, error) {
	row := rs.db.QueryRow(`SELECT id, name, description, steps_json, created_at, updated_at
		FROM escalation_policies WHERE id=?`, id)
	return scanEscalationPolicy(row)
}

// ListEscalationPolicies returns all escalation policies ordered by updated_at desc.
func (rs *RoutingStore) ListEscalationPolicies() ([]EscalationPolicy, error) {
	rows, err := rs.db.Query(`SELECT id, name, description, steps_json, created_at, updated_at
		FROM escalation_policies ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]EscalationPolicy, 0)
	for rows.Next() {
		ep, err := scanEscalationPolicy(rows)
		if err != nil {
			continue
		}
		out = append(out, *ep)
	}
	return out, rows.Err()
}

// DeleteEscalationPolicy deletes an escalation policy by ID.
func (rs *RoutingStore) DeleteEscalationPolicy(id string) error {
	result, err := rs.db.Exec(`DELETE FROM escalation_policies WHERE id=?`, id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// -------------------------------------------------------------------
// Routing resolution
// -------------------------------------------------------------------

// Resolve determines which routing policy applies to ctx and returns a RoutingOutcome.
//
// Precedence rules:
//  1. Non-default policies whose matchers ALL match ctx — ordered by priority DESC; highest wins.
//  2. If nothing matched, the highest-priority IsDefault policy is used (fallback).
//  3. If no policies exist, a bare "no routing policies" outcome is returned.
//
// Explainability fields (Explain.*) describe why the selected policy was chosen.
func (rs *RoutingStore) Resolve(ctx RoutingContext) (RoutingOutcome, error) {
	policies, err := rs.ListRoutingPolicies()
	if err != nil {
		return RoutingOutcome{}, err
	}

	// Sort by priority descending (already guaranteed by the query, but be explicit).
	sort.Slice(policies, func(i, j int) bool {
		return policies[i].Priority > policies[j].Priority
	})

	// Primary matching: non-default policies whose matchers all match.
	var defaultPolicy *RoutingPolicy
	for i := range policies {
		p := &policies[i]
		if p.IsDefault {
			if defaultPolicy == nil || p.Priority > defaultPolicy.Priority {
				defaultPolicy = p
			}
			continue
		}
		if policyMatches(*p, ctx) {
			return rs.buildOutcome(ctx, *p, false, describeMatcher(*p, ctx))
		}
	}

	// Fallback: use default policy.
	if defaultPolicy != nil {
		return rs.buildOutcome(ctx, *defaultPolicy, true, "default policy")
	}

	// No policies configured.
	return RoutingOutcome{
		RuleID:     ctx.RuleID,
		ProbeID:    ctx.ProbeID,
		PolicyID:   "",
		PolicyName: "none",
		Explain: RoutingExplain{
			MatchedBy:    "",
			FallbackUsed: true,
			Reason:       "no routing policies configured",
		},
	}, nil
}

func (rs *RoutingStore) buildOutcome(ctx RoutingContext, p RoutingPolicy, fallback bool, matchedBy string) (RoutingOutcome, error) {
	out := RoutingOutcome{
		RuleID:             ctx.RuleID,
		ProbeID:            ctx.ProbeID,
		PolicyID:           p.ID,
		PolicyName:         p.Name,
		OwnerLabel:         p.OwnerLabel,
		OwnerContact:       p.OwnerContact,
		RunbookURL:         p.RunbookURL,
		EscalationPolicyID: p.EscalationPolicyID,
	}

	reason := "matched by " + matchedBy
	if fallback {
		reason = "no specific policy matched; using default fallback policy"
	}

	out.Explain = RoutingExplain{
		MatchedBy:    matchedBy,
		FallbackUsed: fallback,
		Reason:       reason,
	}

	// Resolve escalation steps if policy references an escalation policy.
	if p.EscalationPolicyID != "" {
		ep, err := rs.GetEscalationPolicy(p.EscalationPolicyID)
		if err == nil && ep != nil {
			out.EscalationSteps = ep.Steps
		}
	}

	return out, nil
}

// -------------------------------------------------------------------
// Scanners
// -------------------------------------------------------------------

func scanRoutingPolicy(s scanner) (*RoutingPolicy, error) {
	var (
		p            RoutingPolicy
		isDefault    int
		matchersJSON string
		createdAt    string
		updatedAt    string
	)
	if err := s.Scan(
		&p.ID, &p.Name, &p.Description, &p.Priority, &isDefault,
		&matchersJSON, &p.OwnerLabel, &p.OwnerContact,
		&p.EscalationPolicyID, &p.RunbookURL,
		&createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	p.IsDefault = isDefault == 1
	_ = json.Unmarshal([]byte(matchersJSON), &p.Matchers)
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if p.Matchers == nil {
		p.Matchers = []RoutingMatcher{}
	}
	return &p, nil
}

func scanEscalationPolicy(s scanner) (*EscalationPolicy, error) {
	var (
		ep        EscalationPolicy
		stepsJSON string
		createdAt string
		updatedAt string
	)
	if err := s.Scan(
		&ep.ID, &ep.Name, &ep.Description, &stepsJSON, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(stepsJSON), &ep.Steps)
	ep.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	ep.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if ep.Steps == nil {
		ep.Steps = []EscalationStep{}
	}
	return &ep, nil
}
