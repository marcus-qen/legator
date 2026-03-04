package modeldock

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/llm"
)

// TrialLLMClient abstracts the LLM call for testability.
type TrialLLMClient interface {
	Complete(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error)
	Name() string
}

// costPerToken holds estimated USD cost per 1K tokens for known providers/models.
// These are rough estimates for illustration; zero means "unknown / free".
var costPerToken = map[string]struct{ input, output float64 }{
	"gpt-4o":        {0.005 / 1000, 0.015 / 1000},
	"gpt-4o-mini":   {0.00015 / 1000, 0.0006 / 1000},
	"gpt-4":         {0.03 / 1000, 0.06 / 1000},
	"gpt-3.5-turbo": {0.0005 / 1000, 0.0015 / 1000},
	"claude-3-opus": {0.015 / 1000, 0.075 / 1000},
}

// EstimateCost returns the estimated USD cost for a completion.
func EstimateCost(model string, promptTokens, completionTokens int) float64 {
	m := strings.ToLower(model)
	for key, rates := range costPerToken {
		if strings.Contains(m, key) {
			return float64(promptTokens)*rates.input + float64(completionTokens)*rates.output
		}
	}
	return 0
}

// trialTask is a unit of work: one prompt × one model.
type trialTask struct {
	prompt TrialPrompt
	model  TrialModel
	client TrialLLMClient
	params TrialParameters
}

// trialTaskResult is the output of a single trialTask execution.
type trialTaskResult struct {
	profileID string
	promptID  string
	resp      *llm.CompletionResponse
	latencyMs int64
	err       error
}

// TrialExecutor fans out a Trial's prompts across its configured models concurrently.
type TrialExecutor struct {
	// providers maps profileID → TrialLLMClient
	providers map[string]TrialLLMClient
}

// NewTrialExecutor creates a TrialExecutor with the given provider map.
func NewTrialExecutor(providers map[string]TrialLLMClient) *TrialExecutor {
	return &TrialExecutor{providers: providers}
}

// Execute runs all prompts against all models in the trial and returns the results.
// It fans out concurrently, respecting context cancellation.
func (te *TrialExecutor) Execute(ctx context.Context, trial *Trial) ([]TrialResult, error) {
	params := trial.Parameters

	timeoutSecs := params.TimeoutSecs
	if timeoutSecs <= 0 {
		timeoutSecs = 120
	}

	retries := params.RetryCount
	if retries < 0 {
		retries = 0
	}

	// Build task list: cartesian product of (prompts × models).
	var tasks []trialTask
	for _, prompt := range trial.Prompts {
		for _, model := range trial.Models {
			client, ok := te.providers[model.ProfileID]
			if !ok {
				continue
			}
			tasks = append(tasks, trialTask{
				prompt: prompt,
				model:  model,
				client: client,
				params: params,
			})
		}
	}

	if len(tasks) == 0 {
		return []TrialResult{}, nil
	}

	rawResults := make(chan trialTaskResult, len(tasks))
	var wg sync.WaitGroup

	for _, task := range tasks {
		wg.Add(1)
		go func(t trialTask) {
			defer wg.Done()
			raw := te.runTask(ctx, t, timeoutSecs, retries)
			rawResults <- raw
		}(task)
	}

	wg.Wait()
	close(rawResults)

	var out []TrialResult
	for raw := range rawResults {
		result := TrialResult{
			RunID:          "", // caller sets this
			ProfileID:      raw.profileID,
			PromptID:       raw.promptID,
			TotalLatencyMs: raw.latencyMs,
			TTFT:           raw.latencyMs, // non-streaming: TTFT ≈ total latency
			CreatedAt:      time.Now().UTC(),
		}
		if raw.err != nil {
			result.Error = raw.err.Error()
		} else if raw.resp != nil {
			result.Response = raw.resp.Content
			result.PromptTokens = raw.resp.PromptTokens
			result.CompletionTokens = raw.resp.CompTokens
			result.TotalTokens = raw.resp.PromptTokens + raw.resp.CompTokens
			result.CostEstimateUSD = EstimateCost(raw.resp.Model, raw.resp.PromptTokens, raw.resp.CompTokens)
		}

		// Score quality against the prompt's evaluation criteria.
		for _, p := range trial.Prompts {
			if p.ID == raw.promptID {
				result.QualityScore = ScoreQuality(result.Response, p.Criteria)
				break
			}
		}

		out = append(out, result)
	}

	if out == nil {
		out = []TrialResult{}
	}
	return out, nil
}

func (te *TrialExecutor) runTask(ctx context.Context, t trialTask, timeoutSecs, retries int) trialTaskResult {
	result := trialTaskResult{
		profileID: t.model.ProfileID,
		promptID:  t.prompt.ID,
	}

	req := &llm.CompletionRequest{
		Messages: buildMessages(t.prompt),
	}
	if t.params.Temperature > 0 {
		req.Temperature = t.params.Temperature
	}
	if t.params.MaxTokens > 0 {
		req.MaxTokens = t.params.MaxTokens
	}

	taskCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
	defer cancel()

	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-taskCtx.Done():
				result.err = fmt.Errorf("timeout after %d retries: %w", attempt, taskCtx.Err())
				return result
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}

		start := time.Now()
		resp, err := t.client.Complete(taskCtx, req)
		elapsed := time.Since(start).Milliseconds()

		if err == nil {
			result.resp = resp
			result.latencyMs = elapsed
			return result
		}
		lastErr = err
	}

	result.err = lastErr
	result.latencyMs = int64(timeoutSecs) * 1000
	return result
}

func buildMessages(p TrialPrompt) []llm.Message {
	var msgs []llm.Message
	if strings.TrimSpace(p.System) != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleSystem, Content: p.System})
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: p.User})
	return msgs
}
