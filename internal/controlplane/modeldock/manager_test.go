package modeldock

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/llm"
)

type usageCapture struct {
	records []UsageRecord
}

func (u *usageCapture) RecordUsage(record UsageRecord) error {
	u.records = append(u.records, record)
	return nil
}

func TestProviderManagerFeatureProviderCapturesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"model": "gpt-test",
			"choices": []map[string]any{
				{
					"message":       map[string]string{"content": "ok"},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]int{
				"prompt_tokens":     11,
				"completion_tokens": 7,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	mgr := NewProviderManager(llm.ProviderConfig{
		Name:    "openai",
		BaseURL: srv.URL,
		APIKey:  "sk-env",
		Model:   "gpt-test",
	})

	recorder := &usageCapture{}
	provider := mgr.Provider(FeatureTask, recorder)

	_, err := provider.Complete(context.Background(), &llm.CompletionRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("complete request: %v", err)
	}

	if len(recorder.records) != 1 {
		t.Fatalf("expected 1 usage record, got %d", len(recorder.records))
	}
	if recorder.records[0].ProfileID != EnvProfileID {
		t.Fatalf("expected env profile id, got %q", recorder.records[0].ProfileID)
	}
	if recorder.records[0].TotalTokens != 18 {
		t.Fatalf("expected total tokens 18, got %d", recorder.records[0].TotalTokens)
	}
}
