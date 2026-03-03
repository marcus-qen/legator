package providerproxy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

var ErrRunIDRequired = errors.New("run_id is required")

// SpendRecord captures usage accounting for one proxied provider call.
type SpendRecord struct {
	ID            string
	RunID         string
	JobID         string
	SessionID     string
	Model         string
	InputTokens   int
	OutputTokens  int
	TotalTokens   int
	EstimatedCost float64
	CreatedAt     time.Time
}

// SpendTotals aggregates spend for a run.
type SpendTotals struct {
	RunID         string  `json:"run_id"`
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	TotalTokens   int     `json:"total_tokens"`
	EstimatedCost float64 `json:"estimated_cost"`
}

// SpendStore persists provider proxy spend data in SQLite.
type SpendStore struct {
	db *sql.DB
}

func NewSpendStore(dbPath string) (*SpendStore, error) {
	path := strings.TrimSpace(dbPath)
	if path == "" {
		path = ":memory:"
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open provider proxy spend db: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS provider_proxy_spend (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		job_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		model TEXT NOT NULL,
		input_tokens INTEGER NOT NULL,
		output_tokens INTEGER NOT NULL,
		total_tokens INTEGER NOT NULL,
		estimated_cost REAL NOT NULL,
		created_at TEXT NOT NULL
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create provider proxy spend table: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_provider_proxy_spend_run_id ON provider_proxy_spend(run_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_provider_proxy_spend_job_id ON provider_proxy_spend(job_id)`)

	return &SpendStore{db: db}, nil
}

func (s *SpendStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SpendStore) Totals(ctx context.Context, runID string) (SpendTotals, error) {
	if s == nil || s.db == nil {
		return SpendTotals{}, fmt.Errorf("provider proxy spend store unavailable")
	}
	rid := strings.TrimSpace(runID)
	if rid == "" {
		return SpendTotals{}, ErrRunIDRequired
	}

	var totals SpendTotals
	totals.RunID = rid
	row := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(total_tokens), 0),
		COALESCE(SUM(estimated_cost), 0)
		FROM provider_proxy_spend
		WHERE run_id = ?`, rid)
	if err := row.Scan(&totals.InputTokens, &totals.OutputTokens, &totals.TotalTokens, &totals.EstimatedCost); err != nil {
		return SpendTotals{}, fmt.Errorf("query provider proxy spend totals: %w", err)
	}
	return totals, nil
}

func (s *SpendStore) Record(ctx context.Context, record SpendRecord) (SpendTotals, error) {
	if s == nil || s.db == nil {
		return SpendTotals{}, fmt.Errorf("provider proxy spend store unavailable")
	}

	rid := strings.TrimSpace(record.RunID)
	if rid == "" {
		return SpendTotals{}, ErrRunIDRequired
	}
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	record.JobID = strings.TrimSpace(record.JobID)
	record.SessionID = strings.TrimSpace(record.SessionID)
	record.Model = strings.TrimSpace(record.Model)
	if record.TotalTokens <= 0 {
		record.TotalTokens = record.InputTokens + record.OutputTokens
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SpendTotals{}, fmt.Errorf("begin provider proxy spend tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO provider_proxy_spend (
		id, run_id, job_id, session_id, model, input_tokens, output_tokens, total_tokens, estimated_cost, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID,
		rid,
		record.JobID,
		record.SessionID,
		record.Model,
		record.InputTokens,
		record.OutputTokens,
		record.TotalTokens,
		record.EstimatedCost,
		record.CreatedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		return SpendTotals{}, fmt.Errorf("insert provider proxy spend record: %w", err)
	}

	var totals SpendTotals
	totals.RunID = rid
	row := tx.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(input_tokens), 0),
		COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(total_tokens), 0),
		COALESCE(SUM(estimated_cost), 0)
		FROM provider_proxy_spend
		WHERE run_id = ?`, rid)
	if err := row.Scan(&totals.InputTokens, &totals.OutputTokens, &totals.TotalTokens, &totals.EstimatedCost); err != nil {
		return SpendTotals{}, fmt.Errorf("query provider proxy spend totals: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return SpendTotals{}, fmt.Errorf("commit provider proxy spend tx: %w", err)
	}
	return totals, nil
}
