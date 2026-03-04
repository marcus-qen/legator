package modeldock

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// ScoreQuality evaluates a model response against the given EvalCriteria.
// Returns a score in [0.0, 1.0].
func ScoreQuality(response string, criteria EvalCriteria) float64 {
	switch criteria.Type {
	case EvalExactMatch:
		if strings.TrimSpace(response) == strings.TrimSpace(criteria.Expected) {
			return 1.0
		}
		return 0.0

	case EvalContains:
		if criteria.Expected == "" {
			return 1.0
		}
		if strings.Contains(response, criteria.Expected) {
			return 1.0
		}
		return 0.0

	case EvalRegex:
		if criteria.Pattern == "" {
			return 1.0
		}
		re, err := regexp.Compile(criteria.Pattern)
		if err != nil {
			return 0.0
		}
		if re.MatchString(response) {
			return 1.0
		}
		return 0.0

	case EvalLLMJudge:
		// Stub: a real implementation would call an LLM to judge quality.
		// Returns 1.0 as a placeholder.
		return 1.0

	default:
		// No criteria specified — treat as unscored (1.0 neutral).
		return 1.0
	}
}

// BuildCompareReport constructs a TrialCompareReport from per-model aggregates.
// trial.Models provides labels for each profile.
func BuildCompareReport(runID, trialID string, trial *Trial, aggs map[string]TrialModelAgg) TrialCompareReport {
	// Apply labels from trial model definitions.
	labeled := make(map[string]TrialModelAgg, len(aggs))
	for _, m := range trial.Models {
		if a, ok := aggs[m.ProfileID]; ok {
			if m.Label != "" {
				a.Label = m.Label
			} else {
				a.Label = m.ProfileID
			}
			labeled[m.ProfileID] = a
		}
	}
	// Include any aggs not in the trial definition (shouldn't happen, but defensive).
	for pid, a := range aggs {
		if _, ok := labeled[pid]; !ok {
			a.Label = pid
			labeled[pid] = a
		}
	}

	models := make([]TrialModelAgg, 0, len(labeled))
	for _, a := range labeled {
		models = append(models, a)
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].ProfileID < models[j].ProfileID
	})

	rankings := map[string][]RankedModel{
		"latency":      rankBy(models, func(a TrialModelAgg) float64 { return a.AvgLatencyMs }, true),
		"ttft":         rankBy(models, func(a TrialModelAgg) float64 { return a.AvgTTFT }, true),
		"quality":      rankBy(models, func(a TrialModelAgg) float64 { return a.AvgQualityScore }, false),
		"cost":         rankBy(models, func(a TrialModelAgg) float64 { return a.TotalCostUSD }, true),
		"total_tokens": rankBy(models, func(a TrialModelAgg) float64 { return float64(a.TotalTokens) }, true),
	}

	return TrialCompareReport{
		RunID:    runID,
		TrialID:  trialID,
		Models:   models,
		Rankings: rankings,
	}
}

// rankBy produces a ranked list of models by the given metric.
// If lowerIsBetter is true, ascending order; otherwise descending.
func rankBy(models []TrialModelAgg, metric func(TrialModelAgg) float64, lowerIsBetter bool) []RankedModel {
	type entry struct {
		agg   TrialModelAgg
		value float64
	}
	entries := make([]entry, len(models))
	for i, m := range models {
		entries[i] = entry{agg: m, value: metric(m)}
	}
	if lowerIsBetter {
		sort.Slice(entries, func(i, j int) bool { return entries[i].value < entries[j].value })
	} else {
		sort.Slice(entries, func(i, j int) bool { return entries[i].value > entries[j].value })
	}
	ranked := make([]RankedModel, len(entries))
	for i, e := range entries {
		ranked[i] = RankedModel{
			Rank:      i + 1,
			ProfileID: e.agg.ProfileID,
			Label:     e.agg.Label,
			Value:     e.value,
		}
	}
	return ranked
}

// ──────────────────────────────────────────────────
// Statistics helpers
// ──────────────────────────────────────────────────

// mean returns the arithmetic mean of values.
func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// percentile returns the p-th percentile of values (0–100).
// Uses the nearest-rank method.
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
