package discovery

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Candidate deployment states form a one-way state machine:
//
//	discovered → approved → deploying → deployed
//	         └→ rejected
//	                     └→ failed
const (
	CandidateStatusDiscovered = "discovered"
	CandidateStatusApproved   = "approved"
	CandidateStatusRejected   = "rejected"
	CandidateStatusDeploying  = "deploying"
	CandidateStatusDeployed   = "deployed"
	CandidateStatusFailed     = "failed"
)

// validTransitions maps each status to the set of statuses it may advance to.
var validTransitions = map[string][]string{
	CandidateStatusDiscovered: {CandidateStatusApproved, CandidateStatusRejected},
	CandidateStatusApproved:   {CandidateStatusDeploying, CandidateStatusRejected},
	CandidateStatusDeploying:  {CandidateStatusDeployed, CandidateStatusFailed},
	CandidateStatusDeployed:   {},
	CandidateStatusRejected:   {},
	CandidateStatusFailed:     {CandidateStatusApproved}, // allow re-approval after failure
}

// ErrInvalidTransition is returned when a status change is not allowed.
var ErrInvalidTransition = errors.New("invalid candidate status transition")

// DeployCandidate is a host discovered by a probe that is a potential target
// for probe installation.
type DeployCandidate struct {
	ID          string    `json:"id"`
	SourceProbe string    `json:"source_probe"`
	IP          string    `json:"ip"`
	Port        int       `json:"port"`
	SSHBanner   string    `json:"ssh_banner,omitempty"`
	OSGuess     string    `json:"os_guess,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	ReportedAt  time.Time `json:"reported_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CandidateStore persists deployment candidates backed by SQLite.
// It shares the database connection of the parent discovery Store.
type CandidateStore struct {
	db *sql.DB
}

// NewCandidateStore initialises the deploy_candidates table in db and returns
// a ready-to-use CandidateStore.
func NewCandidateStore(db *sql.DB) (*CandidateStore, error) {
	if db == nil {
		return nil, fmt.Errorf("nil db")
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS deploy_candidates (
		id          TEXT PRIMARY KEY,
		source_probe TEXT NOT NULL,
		ip          TEXT NOT NULL,
		port        INTEGER NOT NULL DEFAULT 22,
		ssh_banner  TEXT NOT NULL DEFAULT '',
		os_guess    TEXT NOT NULL DEFAULT '',
		fingerprint TEXT NOT NULL DEFAULT '',
		status      TEXT NOT NULL DEFAULT 'discovered',
		error       TEXT NOT NULL DEFAULT '',
		reported_at TEXT NOT NULL,
		updated_at  TEXT NOT NULL
	)`); err != nil {
		return nil, fmt.Errorf("create deploy_candidates: %w", err)
	}

	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_deploy_candidates_ip_port
		ON deploy_candidates(ip, port)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_deploy_candidates_status
		ON deploy_candidates(status)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_deploy_candidates_source
		ON deploy_candidates(source_probe)`)

	return &CandidateStore{db: db}, nil
}

// OpenCandidateStore creates a CandidateStore that shares Store's existing
// database connection.
func (s *Store) OpenCandidateStore() (*CandidateStore, error) {
	return NewCandidateStore(s.db)
}

// Upsert inserts a new candidate or, if the IP+port pair already exists,
// updates the source probe, banner, and OS guess without touching the status.
// Returns the persisted candidate.
func (cs *CandidateStore) Upsert(c *DeployCandidate) (*DeployCandidate, error) {
	if c == nil {
		return nil, fmt.Errorf("nil candidate")
	}
	now := time.Now().UTC()
	if c.ReportedAt.IsZero() {
		c.ReportedAt = now
	}
	if c.ID == "" {
		c.ID = candidateID(c.IP, c.Port)
	}
	if c.Status == "" {
		c.Status = CandidateStatusDiscovered
	}
	if c.Port == 0 {
		c.Port = 22
	}

	_, err := cs.db.Exec(`INSERT INTO deploy_candidates
		(id, source_probe, ip, port, ssh_banner, os_guess, fingerprint, status, error, reported_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ip, port) DO UPDATE SET
			source_probe = excluded.source_probe,
			ssh_banner   = excluded.ssh_banner,
			os_guess     = excluded.os_guess,
			fingerprint  = excluded.fingerprint,
			updated_at   = excluded.updated_at`,
		c.ID,
		c.SourceProbe,
		c.IP,
		c.Port,
		c.SSHBanner,
		c.OSGuess,
		c.Fingerprint,
		c.Status,
		c.Error,
		c.ReportedAt.UTC().Format(time.RFC3339Nano),
		now.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("upsert candidate: %w", err)
	}

	return cs.GetByIPPort(c.IP, c.Port)
}

// Get returns the candidate with the given ID.
func (cs *CandidateStore) Get(id string) (*DeployCandidate, error) {
	row := cs.db.QueryRow(`SELECT id, source_probe, ip, port, ssh_banner, os_guess,
		fingerprint, status, error, reported_at, updated_at
		FROM deploy_candidates WHERE id = ?`, id)
	return scanDeployCandidate(row)
}

// GetByIPPort returns the candidate matching the given IP and port.
func (cs *CandidateStore) GetByIPPort(ip string, port int) (*DeployCandidate, error) {
	row := cs.db.QueryRow(`SELECT id, source_probe, ip, port, ssh_banner, os_guess,
		fingerprint, status, error, reported_at, updated_at
		FROM deploy_candidates WHERE ip = ? AND port = ?`, ip, port)
	return scanDeployCandidate(row)
}

// List returns all candidates, optionally filtered by status.
// Pass an empty string to return all candidates.
func (cs *CandidateStore) List(status string) ([]DeployCandidate, error) {
	var rows *sql.Rows
	var err error

	status = strings.TrimSpace(status)
	if status != "" {
		rows, err = cs.db.Query(`SELECT id, source_probe, ip, port, ssh_banner, os_guess,
			fingerprint, status, error, reported_at, updated_at
			FROM deploy_candidates WHERE status = ?
			ORDER BY reported_at DESC`, status)
	} else {
		rows, err = cs.db.Query(`SELECT id, source_probe, ip, port, ssh_banner, os_guess,
			fingerprint, status, error, reported_at, updated_at
			FROM deploy_candidates
			ORDER BY reported_at DESC`)
	}
	if err != nil {
		return nil, fmt.Errorf("list candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]DeployCandidate, 0)
	for rows.Next() {
		c, err := scanDeployCandidate(rows)
		if err != nil {
			continue
		}
		candidates = append(candidates, *c)
	}
	return candidates, rows.Err()
}

// Transition moves a candidate from its current status to newStatus.
// Returns ErrInvalidTransition if the move is not allowed by the state machine.
func (cs *CandidateStore) Transition(id, newStatus, errMsg string) error {
	current, err := cs.Get(id)
	if err != nil {
		return err
	}

	allowed := false
	for _, s := range validTransitions[current.Status] {
		if s == newStatus {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, current.Status, newStatus)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := cs.db.Exec(`UPDATE deploy_candidates
		SET status = ?, error = ?, updated_at = ? WHERE id = ?`,
		newStatus, errMsg, now, id)
	if err != nil {
		return fmt.Errorf("transition candidate: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// IsCandidateNotFound reports whether err came from a missing candidate.
func IsCandidateNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// candidateID builds a deterministic ID from ip and port.
func candidateID(ip string, port int) string {
	return fmt.Sprintf("cand-%s-%d", strings.ReplaceAll(strings.TrimSpace(ip), ".", "-"), port)
}

type candidateScanner interface {
	Scan(dest ...any) error
}

func scanDeployCandidate(row candidateScanner) (*DeployCandidate, error) {
	var c DeployCandidate
	var reportedAtRaw, updatedAtRaw string

	if err := row.Scan(
		&c.ID, &c.SourceProbe, &c.IP, &c.Port,
		&c.SSHBanner, &c.OSGuess, &c.Fingerprint,
		&c.Status, &c.Error,
		&reportedAtRaw, &updatedAtRaw,
	); err != nil {
		return nil, err
	}
	c.ReportedAt, _ = time.Parse(time.RFC3339Nano, reportedAtRaw)
	c.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAtRaw)
	return &c, nil
}
