package modeldock

import (
	"time"
)

// EvalType defines how trial prompt responses are evaluated.
type EvalType string

const (
	EvalExactMatch EvalType = "exact_match"
	EvalContains   EvalType = "contains"
	EvalRegex      EvalType = "regex"
	EvalLLMJudge   EvalType = "llm_judge" // stub: always returns 1.0
)

// EvalCriteria specifies how a prompt's response is scored.
type EvalCriteria struct {
	Type     EvalType `json:"type"`
	Expected string   `json:"expected,omitempty"` // used by exact_match / contains
	Pattern  string   `json:"pattern,omitempty"`  // used by regex
}

// TrialPrompt is a single prompt in the prompt set.
type TrialPrompt struct {
	ID       string       `json:"id"`
	System   string       `json:"system,omitempty"`
	User     string       `json:"user"`
	Criteria EvalCriteria `json:"criteria,omitempty"`
}

// TrialModel identifies a model configuration to include in a trial.
type TrialModel struct {
	ProfileID string `json:"profile_id"`
	Label     string `json:"label,omitempty"` // human-readable label override
}

// TrialParameters holds LLM generation settings.
type TrialParameters struct {
	Temperature float64 `json:"temperature,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	TimeoutSecs int     `json:"timeout_secs,omitempty"`
	RetryCount  int     `json:"retry_count,omitempty"`
}

// Trial is a named benchmark definition specifying prompts, models, and eval criteria.
type Trial struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Prompts     []TrialPrompt   `json:"prompts"`
	Models      []TrialModel    `json:"models"`
	Parameters  TrialParameters `json:"parameters"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// TrialRunStatus represents the execution state of a TrialRun.
type TrialRunStatus string

const (
	TrialRunPending   TrialRunStatus = "pending"
	TrialRunRunning   TrialRunStatus = "running"
	TrialRunCompleted TrialRunStatus = "completed"
	TrialRunFailed    TrialRunStatus = "failed"
)

// TrialRun is one execution of a Trial.
type TrialRun struct {
	ID          string         `json:"id"`
	TrialID     string         `json:"trial_id"`
	Status      TrialRunStatus `json:"status"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	ErrorMsg    string         `json:"error,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

// TrialResult is the per-model per-prompt outcome of a TrialRun.
type TrialResult struct {
	ID               string    `json:"id"`
	RunID            string    `json:"run_id"`
	ProfileID        string    `json:"profile_id"`
	PromptID         string    `json:"prompt_id"`
	Response         string    `json:"response"`
	TTFT             int64     `json:"ttft_ms"`          // time-to-first-token in ms (approx = total for non-streaming)
	TotalLatencyMs   int64     `json:"total_latency_ms"` // total round-trip in ms
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	CostEstimateUSD  float64   `json:"cost_estimate_usd"`
	QualityScore     float64   `json:"quality_score"` // 0.0–1.0
	Error            string    `json:"error,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// TrialModelAgg aggregates TrialResult metrics for a single model across a run.
type TrialModelAgg struct {
	ProfileID       string  `json:"profile_id"`
	Label           string  `json:"label,omitempty"`
	PromptCount     int     `json:"prompt_count"`
	ErrorCount      int     `json:"error_count"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
	P50LatencyMs    float64 `json:"p50_latency_ms"`
	P95LatencyMs    float64 `json:"p95_latency_ms"`
	P99LatencyMs    float64 `json:"p99_latency_ms"`
	AvgTTFT         float64 `json:"avg_ttft_ms"`
	AvgPromptTokens float64 `json:"avg_prompt_tokens"`
	AvgCompTokens   float64 `json:"avg_completion_tokens"`
	TotalTokens     int     `json:"total_tokens"`
	TotalCostUSD    float64 `json:"total_cost_usd"`
	AvgQualityScore float64 `json:"avg_quality_score"`
}

// TrialCompareReport is the side-by-side comparison of models for a run.
type TrialCompareReport struct {
	RunID    string                   `json:"run_id"`
	TrialID  string                   `json:"trial_id"`
	Models   []TrialModelAgg          `json:"models"`
	Rankings map[string][]RankedModel `json:"rankings"` // metric -> ranked list
}

// RankedModel pairs a profile with its rank for a specific metric.
type RankedModel struct {
	Rank      int     `json:"rank"`
	ProfileID string  `json:"profile_id"`
	Label     string  `json:"label,omitempty"`
	Value     float64 `json:"value"`
}
