package modeldock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/llm"
)

// mockLLMClient is a controllable TrialLLMClient for testing.
type mockLLMClient struct {
	name     string
	latency  time.Duration
	response string
	err      error
	calls    int
}

func (m *mockLLMClient) Name() string { return m.name }

func (m *mockLLMClient) Complete(ctx context.Context, _ *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	m.calls++
	if m.latency > 0 {
		select {
		case <-time.After(m.latency):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.err != nil {
		return nil, m.err
	}
	return &llm.CompletionResponse{
		Content:      m.response,
		Model:        m.name,
		FinishReason: "stop",
		PromptTokens: 10,
		CompTokens:   5,
	}, nil
}

func makeExecTrial() *Trial {
	return &Trial{
		ID:   "trial-exec-test",
		Name: "Executor Test",
		Prompts: []TrialPrompt{
			{ID: "p1", User: "What is 2+2?", Criteria: EvalCriteria{Type: EvalContains, Expected: "4"}},
			{ID: "p2", User: "Capital of France?", Criteria: EvalCriteria{Type: EvalContains, Expected: "Paris"}},
		},
		Models: []TrialModel{
			{ProfileID: "fast-model"},
			{ProfileID: "slow-model"},
		},
		Parameters: TrialParameters{TimeoutSecs: 10},
	}
}

func TestTrialExecutor_FanOut(t *testing.T) {
	providers := map[string]TrialLLMClient{
		"fast-model": &mockLLMClient{name: "fast", response: "4"},
		"slow-model": &mockLLMClient{name: "slow", response: "Paris"},
	}
	exec := NewTrialExecutor(providers)
	trial := makeExecTrial()

	results, err := exec.Execute(context.Background(), trial)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// 2 prompts × 2 models = 4 results.
	if len(results) != 4 {
		t.Errorf("want 4 results, got %d", len(results))
	}
}

func TestTrialExecutor_PartialError(t *testing.T) {
	providers := map[string]TrialLLMClient{
		"good-model": &mockLLMClient{name: "good", response: "42"},
		"bad-model":  &mockLLMClient{name: "bad", err: errors.New("model unavailable")},
	}
	exec := NewTrialExecutor(providers)
	trial := &Trial{
		ID: "t1",
		Prompts: []TrialPrompt{
			{ID: "p1", User: "Hello"},
		},
		Models: []TrialModel{
			{ProfileID: "good-model"},
			{ProfileID: "bad-model"},
		},
		Parameters: TrialParameters{TimeoutSecs: 5},
	}

	results, err := exec.Execute(context.Background(), trial)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("want 2 results, got %d", len(results))
	}
	var errCount int
	for _, r := range results {
		if r.Error != "" {
			errCount++
		}
	}
	if errCount != 1 {
		t.Errorf("want 1 error result, got %d", errCount)
	}
}

func TestTrialExecutor_MissingProvider(t *testing.T) {
	// A trial that references a model with no provider registered.
	providers := map[string]TrialLLMClient{
		"known": &mockLLMClient{name: "known", response: "ok"},
	}
	exec := NewTrialExecutor(providers)
	trial := &Trial{
		ID: "t2",
		Prompts: []TrialPrompt{
			{ID: "p1", User: "Hello"},
		},
		Models: []TrialModel{
			{ProfileID: "known"},
			{ProfileID: "unknown-profile"},
		},
		Parameters: TrialParameters{TimeoutSecs: 5},
	}

	results, err := exec.Execute(context.Background(), trial)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only the known provider generates a result.
	if len(results) != 1 {
		t.Errorf("want 1 result (unknown skipped), got %d", len(results))
	}
}

func TestTrialExecutor_ContextCancellation(t *testing.T) {
	providers := map[string]TrialLLMClient{
		"slow": &mockLLMClient{name: "slow", latency: 2 * time.Second, response: "ok"},
	}
	exec := NewTrialExecutor(providers)
	trial := &Trial{
		ID: "t3",
		Prompts: []TrialPrompt{
			{ID: "p1", User: "Hello"},
		},
		Models:     []TrialModel{{ProfileID: "slow"}},
		Parameters: TrialParameters{TimeoutSecs: 5},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	results, err := exec.Execute(ctx, trial)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("want 1 result, got %d", len(results))
	}
	if results[0].Error == "" {
		t.Error("expected error in result due to cancellation")
	}
}

func TestTrialExecutor_ConcurrencyLatency(t *testing.T) {
	// With 4 tasks that each take 50ms, concurrent execution should finish in ~100ms (not 200ms).
	providers := map[string]TrialLLMClient{
		"m1": &mockLLMClient{name: "m1", latency: 50 * time.Millisecond, response: "ok"},
		"m2": &mockLLMClient{name: "m2", latency: 50 * time.Millisecond, response: "ok"},
	}
	exec := NewTrialExecutor(providers)
	trial := &Trial{
		ID: "t4",
		Prompts: []TrialPrompt{
			{ID: "p1", User: "Q1"},
			{ID: "p2", User: "Q2"},
		},
		Models:     []TrialModel{{ProfileID: "m1"}, {ProfileID: "m2"}},
		Parameters: TrialParameters{TimeoutSecs: 10},
	}

	start := time.Now()
	results, err := exec.Execute(context.Background(), trial)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(results) != 4 {
		t.Errorf("want 4 results, got %d", len(results))
	}
	// Concurrent execution: 4 tasks at 50ms each should complete well under 200ms.
	if elapsed > 180*time.Millisecond {
		t.Errorf("execution took %v, expected concurrent execution (~50-100ms)", elapsed)
	}
}

func TestTrialExecutor_QualityScoring(t *testing.T) {
	providers := map[string]TrialLLMClient{
		"m": &mockLLMClient{name: "m", response: "The answer is 4"},
	}
	exec := NewTrialExecutor(providers)
	trial := &Trial{
		ID: "t5",
		Prompts: []TrialPrompt{
			{
				ID:       "p1",
				User:     "2+2?",
				Criteria: EvalCriteria{Type: EvalContains, Expected: "4"},
			},
		},
		Models:     []TrialModel{{ProfileID: "m"}},
		Parameters: TrialParameters{TimeoutSecs: 5},
	}

	results, _ := exec.Execute(context.Background(), trial)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].QualityScore != 1.0 {
		t.Errorf("quality score: want 1.0, got %v", results[0].QualityScore)
	}
}

func TestEstimateCost(t *testing.T) {
	cases := []struct {
		model   string
		prompt  int
		comp    int
		wantPos bool // just check > 0 for known models
	}{
		{"gpt-4o-mini", 100, 50, true},
		{"gpt-4o", 100, 50, true},
		{"unknown-model-xyz", 100, 50, false},
	}
	for _, tc := range cases {
		cost := EstimateCost(tc.model, tc.prompt, tc.comp)
		if tc.wantPos && cost <= 0 {
			t.Errorf("EstimateCost(%q): want > 0, got %v", tc.model, cost)
		}
		if !tc.wantPos && cost != 0 {
			t.Errorf("EstimateCost(%q): want 0, got %v", tc.model, cost)
		}
	}
}
