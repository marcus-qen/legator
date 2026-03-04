package modeldock

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TrialStore persists trial definitions, runs, and results using SQLite.
type TrialStore struct {
	db *sql.DB
}

// NewTrialStore creates a TrialStore backed by the given *sql.DB and ensures
// that the required tables exist.
func NewTrialStore(db *sql.DB) (*TrialStore, error) {
	ts := &TrialStore{db: db}
	if err := ts.migrate(); err != nil {
		return nil, fmt.Errorf("trial store migrate: %w", err)
	}
	return ts, nil
}

func (ts *TrialStore) migrate() error {
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS trial_definitions (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			prompts_json TEXT NOT NULL DEFAULT '[]',
			models_json  TEXT NOT NULL DEFAULT '[]',
			params_json  TEXT NOT NULL DEFAULT '{}',
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS trial_runs (
			id           TEXT PRIMARY KEY,
			trial_id     TEXT NOT NULL,
			status       TEXT NOT NULL DEFAULT 'pending',
			started_at   TEXT,
			completed_at TEXT,
			error_msg    TEXT NOT NULL DEFAULT '',
			created_at   TEXT NOT NULL,
			FOREIGN KEY (trial_id) REFERENCES trial_definitions(id)
		)`,
		`CREATE TABLE IF NOT EXISTS trial_results (
			id                TEXT PRIMARY KEY,
			run_id            TEXT NOT NULL,
			profile_id        TEXT NOT NULL,
			prompt_id         TEXT NOT NULL,
			response          TEXT NOT NULL DEFAULT '',
			ttft_ms           INTEGER NOT NULL DEFAULT 0,
			total_latency_ms  INTEGER NOT NULL DEFAULT 0,
			prompt_tokens     INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens      INTEGER NOT NULL DEFAULT 0,
			cost_estimate_usd REAL NOT NULL DEFAULT 0,
			quality_score     REAL NOT NULL DEFAULT 0,
			error_msg         TEXT NOT NULL DEFAULT '',
			created_at        TEXT NOT NULL,
			FOREIGN KEY (run_id) REFERENCES trial_runs(id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_trial_runs_trial_id ON trial_runs(trial_id)`,
		`CREATE INDEX IF NOT EXISTS idx_trial_results_run_id ON trial_results(run_id)`,
		`CREATE INDEX IF NOT EXISTS idx_trial_results_profile_id ON trial_results(profile_id)`,
	}
	for _, stmt := range ddl {
		if _, err := ts.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate trial store: %w (stmt: %.60s)", err, stmt)
		}
	}
	return nil
}

// ──────────────────────────────────────────────────
// Trial CRUD
// ──────────────────────────────────────────────────

func (ts *TrialStore) CreateTrial(t Trial) (*Trial, error) {
	now := time.Now().UTC()
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	t.CreatedAt = now
	t.UpdatedAt = now

	promptsJSON, err := json.Marshal(t.Prompts)
	if err != nil {
		return nil, fmt.Errorf("marshal prompts: %w", err)
	}
	modelsJSON, err := json.Marshal(t.Models)
	if err != nil {
		return nil, fmt.Errorf("marshal models: %w", err)
	}
	paramsJSON, err := json.Marshal(t.Parameters)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}

	_, err = ts.db.Exec(`INSERT INTO trial_definitions
		(id, name, description, prompts_json, models_json, params_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Description,
		string(promptsJSON), string(modelsJSON), string(paramsJSON),
		t.CreatedAt.Format(time.RFC3339Nano),
		t.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("insert trial: %w", err)
	}
	return ts.GetTrial(t.ID)
}

func (ts *TrialStore) GetTrial(id string) (*Trial, error) {
	row := ts.db.QueryRow(`SELECT id, name, description, prompts_json, models_json, params_json, created_at, updated_at
		FROM trial_definitions WHERE id = ?`, id)
	return scanTrial(row)
}

func (ts *TrialStore) ListTrials() ([]Trial, error) {
	rows, err := ts.db.Query(`SELECT id, name, description, prompts_json, models_json, params_json, created_at, updated_at
		FROM trial_definitions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Trial
	for rows.Next() {
		t, err := scanTrial(rows)
		if err != nil {
			continue
		}
		out = append(out, *t)
	}
	if out == nil {
		out = []Trial{}
	}
	return out, rows.Err()
}

func scanTrial(row interface{ Scan(...any) error }) (*Trial, error) {
	var (
		t                                   Trial
		promptsJSON, modelsJSON, paramsJSON string
		createdAt, updatedAt                string
	)
	if err := row.Scan(&t.ID, &t.Name, &t.Description, &promptsJSON, &modelsJSON, &paramsJSON, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(promptsJSON), &t.Prompts); err != nil {
		t.Prompts = []TrialPrompt{}
	}
	if err := json.Unmarshal([]byte(modelsJSON), &t.Models); err != nil {
		t.Models = []TrialModel{}
	}
	_ = json.Unmarshal([]byte(paramsJSON), &t.Parameters)
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &t, nil
}

// ──────────────────────────────────────────────────
// TrialRun CRUD
// ──────────────────────────────────────────────────

func (ts *TrialStore) CreateRun(trialID string) (*TrialRun, error) {
	now := time.Now().UTC()
	run := TrialRun{
		ID:        uuid.NewString(),
		TrialID:   trialID,
		Status:    TrialRunPending,
		CreatedAt: now,
	}
	_, err := ts.db.Exec(`INSERT INTO trial_runs (id, trial_id, status, error_msg, created_at)
		VALUES (?, ?, ?, '', ?)`,
		run.ID, run.TrialID, string(run.Status),
		run.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("create run: %w", err)
	}
	return ts.GetRun(run.ID)
}

func (ts *TrialStore) GetRun(id string) (*TrialRun, error) {
	row := ts.db.QueryRow(`SELECT id, trial_id, status, started_at, completed_at, error_msg, created_at
		FROM trial_runs WHERE id = ?`, id)
	return scanRun(row)
}

func (ts *TrialStore) ListRuns(trialID string) ([]TrialRun, error) {
	rows, err := ts.db.Query(`SELECT id, trial_id, status, started_at, completed_at, error_msg, created_at
		FROM trial_runs WHERE trial_id = ? ORDER BY created_at DESC`, trialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TrialRun
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			continue
		}
		out = append(out, *run)
	}
	if out == nil {
		out = []TrialRun{}
	}
	return out, rows.Err()
}

func (ts *TrialStore) UpdateRunStatus(id string, status TrialRunStatus, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch status {
	case TrialRunRunning:
		_, err := ts.db.Exec(`UPDATE trial_runs SET status = ?, started_at = ? WHERE id = ?`,
			string(status), now, id)
		return err
	case TrialRunCompleted, TrialRunFailed:
		_, err := ts.db.Exec(`UPDATE trial_runs SET status = ?, completed_at = ?, error_msg = ? WHERE id = ?`,
			string(status), now, errMsg, id)
		return err
	default:
		_, err := ts.db.Exec(`UPDATE trial_runs SET status = ? WHERE id = ?`, string(status), id)
		return err
	}
}

func scanRun(row interface{ Scan(...any) error }) (*TrialRun, error) {
	var (
		run                               TrialRun
		startedAt, completedAt, createdAt sql.NullString
		errMsg                            string
	)
	if err := row.Scan(&run.ID, &run.TrialID, &run.Status, &startedAt, &completedAt, &errMsg, &createdAt); err != nil {
		return nil, err
	}
	run.ErrorMsg = errMsg
	run.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt.String)
	if startedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, startedAt.String)
		run.StartedAt = &t
	}
	if completedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, completedAt.String)
		run.CompletedAt = &t
	}
	return &run, nil
}

// ──────────────────────────────────────────────────
// TrialResult CRUD
// ──────────────────────────────────────────────────

func (ts *TrialStore) SaveResult(r TrialResult) (*TrialResult, error) {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	r.TotalTokens = r.PromptTokens + r.CompletionTokens
	_, err := ts.db.Exec(`INSERT INTO trial_results
		(id, run_id, profile_id, prompt_id, response, ttft_ms, total_latency_ms,
		 prompt_tokens, completion_tokens, total_tokens, cost_estimate_usd, quality_score, error_msg, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.RunID, r.ProfileID, r.PromptID, r.Response,
		r.TTFT, r.TotalLatencyMs,
		r.PromptTokens, r.CompletionTokens, r.TotalTokens,
		r.CostEstimateUSD, r.QualityScore, r.Error,
		r.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("save result: %w", err)
	}
	return &r, nil
}

func (ts *TrialStore) ListResults(runID string) ([]TrialResult, error) {
	rows, err := ts.db.Query(`SELECT id, run_id, profile_id, prompt_id, response, ttft_ms, total_latency_ms,
		prompt_tokens, completion_tokens, total_tokens, cost_estimate_usd, quality_score, error_msg, created_at
		FROM trial_results WHERE run_id = ? ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TrialResult
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			continue
		}
		out = append(out, *r)
	}
	if out == nil {
		out = []TrialResult{}
	}
	return out, rows.Err()
}

func (ts *TrialStore) ListResultsByTrial(trialID string) ([]TrialResult, error) {
	rows, err := ts.db.Query(`SELECT r.id, r.run_id, r.profile_id, r.prompt_id, r.response, r.ttft_ms, r.total_latency_ms,
		r.prompt_tokens, r.completion_tokens, r.total_tokens, r.cost_estimate_usd, r.quality_score, r.error_msg, r.created_at
		FROM trial_results r
		JOIN trial_runs run ON run.id = r.run_id
		WHERE run.trial_id = ?
		ORDER BY r.created_at`, trialID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TrialResult
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			continue
		}
		out = append(out, *r)
	}
	if out == nil {
		out = []TrialResult{}
	}
	return out, rows.Err()
}

// AggregateByModel returns per-model aggregation for a single run.
func (ts *TrialStore) AggregateByModel(runID string) (map[string]TrialModelAgg, error) {
	results, err := ts.ListResults(runID)
	if err != nil {
		return nil, err
	}
	return aggregateResults(results), nil
}

func aggregateResults(results []TrialResult) map[string]TrialModelAgg {
	type accumulator struct {
		latencies []float64
		ttfts     []float64
		agg       TrialModelAgg
	}
	acc := make(map[string]*accumulator)
	for _, r := range results {
		a, ok := acc[r.ProfileID]
		if !ok {
			a = &accumulator{agg: TrialModelAgg{ProfileID: r.ProfileID}}
			acc[r.ProfileID] = a
		}
		a.agg.PromptCount++
		if r.Error != "" {
			a.agg.ErrorCount++
		} else {
			a.latencies = append(a.latencies, float64(r.TotalLatencyMs))
			a.ttfts = append(a.ttfts, float64(r.TTFT))
		}
		a.agg.TotalTokens += r.TotalTokens
		a.agg.TotalCostUSD += r.CostEstimateUSD
		a.agg.AvgQualityScore += r.QualityScore
		a.agg.AvgPromptTokens += float64(r.PromptTokens)
		a.agg.AvgCompTokens += float64(r.CompletionTokens)
	}

	out := make(map[string]TrialModelAgg, len(acc))
	for pid, a := range acc {
		n := float64(a.agg.PromptCount)
		if n > 0 {
			a.agg.AvgQualityScore /= n
			a.agg.AvgPromptTokens /= n
			a.agg.AvgCompTokens /= n
		}
		if len(a.latencies) > 0 {
			a.agg.AvgLatencyMs = mean(a.latencies)
			a.agg.P50LatencyMs = percentile(a.latencies, 50)
			a.agg.P95LatencyMs = percentile(a.latencies, 95)
			a.agg.P99LatencyMs = percentile(a.latencies, 99)
		}
		if len(a.ttfts) > 0 {
			a.agg.AvgTTFT = mean(a.ttfts)
		}
		out[pid] = a.agg
	}
	return out
}

func scanResult(row interface{ Scan(...any) error }) (*TrialResult, error) {
	var (
		r         TrialResult
		createdAt string
	)
	if err := row.Scan(
		&r.ID, &r.RunID, &r.ProfileID, &r.PromptID, &r.Response,
		&r.TTFT, &r.TotalLatencyMs,
		&r.PromptTokens, &r.CompletionTokens, &r.TotalTokens,
		&r.CostEstimateUSD, &r.QualityScore, &r.Error, &createdAt,
	); err != nil {
		return nil, err
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return &r, nil
}
