package modeldock

import (
	"testing"
)

// ── ScoreQuality ────────────────────────────────────────────

func TestScoreQuality_ExactMatch(t *testing.T) {
	c := EvalCriteria{Type: EvalExactMatch, Expected: "Paris"}
	if ScoreQuality("Paris", c) != 1.0 {
		t.Error("exact match hit: want 1.0")
	}
	if ScoreQuality("paris", c) != 0.0 {
		t.Error("exact match miss (case): want 0.0")
	}
	if ScoreQuality("Paris is great", c) != 0.0 {
		t.Error("exact match miss (contains): want 0.0")
	}
	// Whitespace trimming.
	if ScoreQuality("  Paris  ", c) != 1.0 {
		t.Error("exact match with whitespace: want 1.0")
	}
}

func TestScoreQuality_Contains(t *testing.T) {
	c := EvalCriteria{Type: EvalContains, Expected: "Paris"}
	if ScoreQuality("The capital is Paris.", c) != 1.0 {
		t.Error("contains hit: want 1.0")
	}
	if ScoreQuality("The capital is Lyon.", c) != 0.0 {
		t.Error("contains miss: want 0.0")
	}
	// Empty expected — always passes.
	if ScoreQuality("anything", EvalCriteria{Type: EvalContains}) != 1.0 {
		t.Error("empty expected: want 1.0")
	}
}

func TestScoreQuality_Regex(t *testing.T) {
	c := EvalCriteria{Type: EvalRegex, Pattern: `^\d+$`}
	if ScoreQuality("42", c) != 1.0 {
		t.Error("regex match: want 1.0")
	}
	if ScoreQuality("42abc", c) != 0.0 {
		t.Error("regex no match: want 0.0")
	}
	// Bad pattern.
	bad := EvalCriteria{Type: EvalRegex, Pattern: `[invalid`}
	if ScoreQuality("anything", bad) != 0.0 {
		t.Error("bad regex: want 0.0")
	}
	// Empty pattern — always passes.
	if ScoreQuality("anything", EvalCriteria{Type: EvalRegex}) != 1.0 {
		t.Error("empty pattern: want 1.0")
	}
}

func TestScoreQuality_LLMJudge(t *testing.T) {
	c := EvalCriteria{Type: EvalLLMJudge}
	if ScoreQuality("anything", c) != 1.0 {
		t.Error("llm_judge stub: want 1.0")
	}
}

func TestScoreQuality_NoType(t *testing.T) {
	// Unknown / empty type returns 1.0 (neutral).
	if ScoreQuality("whatever", EvalCriteria{}) != 1.0 {
		t.Error("no criteria: want 1.0 neutral")
	}
}

// ── Percentile / Mean ───────────────────────────────────────

func TestPercentile(t *testing.T) {
	values := []float64{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000}

	p50 := percentile(values, 50)
	if p50 < 450 || p50 > 600 {
		t.Errorf("p50: want ~500, got %v", p50)
	}

	p99 := percentile(values, 99)
	if p99 != 1000 {
		t.Errorf("p99: want 1000, got %v", p99)
	}

	p0 := percentile(values, 0)
	if p0 != 100 {
		t.Errorf("p0: want 100, got %v", p0)
	}

	p100 := percentile(values, 100)
	if p100 != 1000 {
		t.Errorf("p100: want 1000, got %v", p100)
	}
}

func TestPercentile_Empty(t *testing.T) {
	if percentile(nil, 50) != 0 {
		t.Error("empty: want 0")
	}
}

func TestMean(t *testing.T) {
	if mean([]float64{1, 2, 3, 4, 5}) != 3.0 {
		t.Error("mean [1..5]: want 3.0")
	}
	if mean(nil) != 0 {
		t.Error("empty: want 0")
	}
}

// ── BuildCompareReport + rankBy ─────────────────────────────

func TestBuildCompareReport_Rankings(t *testing.T) {
	trial := &Trial{
		ID: "t1",
		Models: []TrialModel{
			{ProfileID: "fast", Label: "Fast Model"},
			{ProfileID: "slow", Label: "Slow Model"},
		},
	}
	aggs := map[string]TrialModelAgg{
		"fast": {ProfileID: "fast", AvgLatencyMs: 100, AvgQualityScore: 0.8, TotalCostUSD: 0.01, TotalTokens: 100},
		"slow": {ProfileID: "slow", AvgLatencyMs: 500, AvgQualityScore: 0.9, TotalCostUSD: 0.05, TotalTokens: 200},
	}

	report := BuildCompareReport("run-1", "t1", trial, aggs)

	if report.RunID != "run-1" {
		t.Errorf("run_id: %s", report.RunID)
	}
	if len(report.Models) != 2 {
		t.Errorf("models: want 2, got %d", len(report.Models))
	}

	// Latency ranking: lower is better → fast first.
	latRank := report.Rankings["latency"]
	if len(latRank) != 2 {
		t.Fatalf("latency ranking: want 2, got %d", len(latRank))
	}
	if latRank[0].ProfileID != "fast" {
		t.Errorf("latency rank 1: want fast, got %s", latRank[0].ProfileID)
	}

	// Quality ranking: higher is better → slow first.
	qualRank := report.Rankings["quality"]
	if qualRank[0].ProfileID != "slow" {
		t.Errorf("quality rank 1: want slow, got %s", qualRank[0].ProfileID)
	}

	// Cost ranking: lower is better → fast first.
	costRank := report.Rankings["cost"]
	if costRank[0].ProfileID != "fast" {
		t.Errorf("cost rank 1: want fast, got %s", costRank[0].ProfileID)
	}
}

func TestBuildCompareReport_Labels(t *testing.T) {
	trial := &Trial{
		ID: "t2",
		Models: []TrialModel{
			{ProfileID: "m1", Label: "My GPT"},
		},
	}
	aggs := map[string]TrialModelAgg{
		"m1": {ProfileID: "m1", AvgLatencyMs: 200},
	}

	report := BuildCompareReport("run-2", "t2", trial, aggs)
	if len(report.Models) != 1 {
		t.Fatalf("want 1 model, got %d", len(report.Models))
	}
	if report.Models[0].Label != "My GPT" {
		t.Errorf("label: want 'My GPT', got %q", report.Models[0].Label)
	}
}

func TestRankBy_SingleModel(t *testing.T) {
	models := []TrialModelAgg{
		{ProfileID: "only", AvgLatencyMs: 300},
	}
	ranked := rankBy(models, func(a TrialModelAgg) float64 { return a.AvgLatencyMs }, true)
	if len(ranked) != 1 {
		t.Fatalf("want 1, got %d", len(ranked))
	}
	if ranked[0].Rank != 1 {
		t.Errorf("rank: want 1, got %d", ranked[0].Rank)
	}
}

func TestRankBy_Ties(t *testing.T) {
	models := []TrialModelAgg{
		{ProfileID: "a", AvgLatencyMs: 100},
		{ProfileID: "b", AvgLatencyMs: 100},
	}
	ranked := rankBy(models, func(a TrialModelAgg) float64 { return a.AvgLatencyMs }, true)
	if len(ranked) != 2 {
		t.Fatalf("want 2, got %d", len(ranked))
	}
	// Both have value 100; order may vary but values must match.
	for _, r := range ranked {
		if r.Value != 100 {
			t.Errorf("tied value: want 100, got %v", r.Value)
		}
	}
}
