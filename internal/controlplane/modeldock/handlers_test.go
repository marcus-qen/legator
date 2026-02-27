package modeldock

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/llm"
)

func newTestHandler(t *testing.T) (*Handler, *Store) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "modeldock.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr := NewProviderManager(llm.ProviderConfig{
		Name:    "openai",
		BaseURL: "https://api.openai.com/v1",
		Model:   "gpt-4o-mini",
		APIKey:  "sk-env",
	})

	h := NewHandler(store, mgr, func() *Profile {
		return &Profile{
			ID:       EnvProfileID,
			Name:     "Environment (LEGATOR_LLM_*)",
			Provider: "openai",
			BaseURL:  "https://api.openai.com/v1",
			Model:    "gpt-4o-mini",
			APIKey:   "sk-env-secret",
		}
	})

	return h, store
}

func TestHandleGetActiveProfileFallsBackToEnv(t *testing.T) {
	h, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/model-profiles/active", nil)
	rr := httptest.NewRecorder()
	h.HandleGetActiveProfile(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "sk-env-secret") {
		t.Fatalf("response leaked raw env api key: %s", body)
	}
	if !strings.Contains(body, "\"source\":\"env\"") {
		t.Fatalf("expected env source in response: %s", body)
	}
}

func TestHandleCreateAndListProfilesNeverReturnRawAPIKey(t *testing.T) {
	h, _ := newTestHandler(t)

	payload := map[string]any{
		"name":      "Team OpenAI",
		"provider":  "openai",
		"base_url":  "https://api.openai.com/v1",
		"model":     "gpt-4.1-mini",
		"api_key":   "sk-top-secret",
		"is_active": true,
	}
	data, _ := json.Marshal(payload)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/model-profiles", bytes.NewReader(data))
	createRR := httptest.NewRecorder()
	h.HandleCreateProfile(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", createRR.Code, createRR.Body.String())
	}
	if strings.Contains(createRR.Body.String(), "sk-top-secret") {
		t.Fatalf("create response leaked raw key: %s", createRR.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/model-profiles", nil)
	listRR := httptest.NewRecorder()
	h.HandleListProfiles(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRR.Code)
	}
	if strings.Contains(listRR.Body.String(), "sk-top-secret") {
		t.Fatalf("list response leaked raw key: %s", listRR.Body.String())
	}
	if !strings.Contains(listRR.Body.String(), "api_key_masked") {
		t.Fatalf("expected masked key field in list response: %s", listRR.Body.String())
	}
}
