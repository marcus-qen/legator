package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

// mockOpenAIServer returns a test server that responds like OpenAI.
func mockOpenAIServer(responses []string) *httptest.Server {
	callIdx := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if callIdx >= len(responses) {
			http.Error(w, "no more responses", 500)
			return
		}
		content := responses[callIdx]
		callIdx++

		resp := openAIResponse{
			Model: "test-model",
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			}{
				{
					Message: struct {
						Content string `json:"content"`
					}{Content: content},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			}{PromptTokens: 100, CompletionTokens: 50},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestOpenAIProviderComplete(t *testing.T) {
	srv := mockOpenAIServer([]string{"Hello, world!"})
	defer srv.Close()

	provider := NewOpenAIProvider(ProviderConfig{
		Name:    "test",
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	resp, err := provider.Complete(context.Background(), &CompletionRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %q", resp.Content)
	}
	if resp.PromptTokens != 100 {
		t.Errorf("expected 100 prompt tokens, got %d", resp.PromptTokens)
	}
}

func TestOpenAIProviderErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"invalid key"}}`, 401)
	}))
	defer srv.Close()

	provider := NewOpenAIProvider(ProviderConfig{
		Name:    "test",
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	_, err := provider.Complete(context.Background(), &CompletionRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestTaskRunnerSingleStep(t *testing.T) {
	// LLM responds with a command, then a summary
	srv := mockOpenAIServer([]string{
		`{"command": "hostname", "args": [], "reason": "Check the hostname"}`,
		"The server hostname is test-server.",
	})
	defer srv.Close()

	provider := NewOpenAIProvider(ProviderConfig{
		Name:    "test",
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	dispatched := 0
	dispatch := func(probeID string, cmd *protocol.CommandPayload) (*protocol.CommandResultPayload, error) {
		dispatched++
		return &protocol.CommandResultPayload{
			RequestID: cmd.RequestID,
			ExitCode:  0,
			Stdout:    "test-server",
			Duration:  5,
		}, nil
	}

	runner := NewTaskRunner(provider, dispatch, nil)
	// Give it a no-op logger
	runner.logger = noopLogger()

	result, err := runner.Run(
		context.Background(),
		"probe-1",
		"What is the hostname?",
		&protocol.InventoryPayload{Hostname: "test-server", OS: "linux", CPUs: 4},
		protocol.CapObserve,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dispatched != 1 {
		t.Errorf("expected 1 dispatch, got %d", dispatched)
	}
	if len(result.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(result.Steps))
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if result.Steps[0].Command != "hostname" {
		t.Errorf("expected 'hostname' command, got %q", result.Steps[0].Command)
	}
}

func TestTaskRunnerImmediateSummary(t *testing.T) {
	// LLM responds with just a summary (no commands needed)
	srv := mockOpenAIServer([]string{
		"Based on the inventory, the server has 4 CPUs and is running Linux.",
	})
	defer srv.Close()

	provider := NewOpenAIProvider(ProviderConfig{
		Name:    "test",
		BaseURL: srv.URL,
		Model:   "test-model",
	})

	dispatched := 0
	dispatch := func(probeID string, cmd *protocol.CommandPayload) (*protocol.CommandResultPayload, error) {
		dispatched++
		return nil, nil
	}

	runner := NewTaskRunner(provider, dispatch, nil)
	runner.logger = noopLogger()

	result, err := runner.Run(
		context.Background(),
		"probe-1",
		"How many CPUs?",
		&protocol.InventoryPayload{Hostname: "test", OS: "linux", CPUs: 4},
		protocol.CapObserve,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dispatched != 0 {
		t.Errorf("expected 0 dispatches, got %d", dispatched)
	}
	if len(result.Steps) != 0 {
		t.Errorf("expected 0 steps, got %d", len(result.Steps))
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
}

func noopLogger() *zap.Logger {
	cfg := zap.NewProductionConfig()
	cfg.OutputPaths = []string{}
	cfg.ErrorOutputPaths = []string{}
	l, _ := cfg.Build()
	return l
}
