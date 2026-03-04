package modeldock

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/llm"
)

// newTestHandlerWithTrials creates a Handler with an in-memory trial store.
func newTestHandlerWithTrials(t *testing.T) (*Handler, *Store, *TrialStore) {
	t.Helper()
	h, store := newTestHandler(t)

	db := newTestTrialDB(t)
	ts, err := NewTrialStore(db)
	if err != nil {
		t.Fatalf("new trial store: %v", err)
	}
	h.trialStore = ts
	return h, store, ts
}

// createProfileInStore inserts a profile and returns its ID.
func createProfileInStore(t *testing.T, store *Store, name string) string {
	t.Helper()
	p, err := store.CreateProfile(Profile{
		Name:     name,
		Provider: "openai",
		BaseURL:  "http://test.local/v1",
		Model:    "gpt-test",
		APIKey:   "sk-test",
		IsActive: false,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return p.ID
}

func trialPayload(profileID string) map[string]any {
	return map[string]any{
		"name":        "API Test Trial",
		"description": "Created by handler test",
		"prompts": []map[string]any{
			{"id": "p1", "user": "What is 2+2?", "criteria": map[string]any{"type": "contains", "expected": "4"}},
		},
		"models": []map[string]any{
			{"profile_id": profileID},
		},
		"parameters": map[string]any{"timeout_secs": 5},
	}
}

func postJSON(t *testing.T, h http.HandlerFunc, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

func getJSON(t *testing.T, h http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

// ── HandleCreateTrial ───────────────────────────────────────

func TestHandleCreateTrial_OK(t *testing.T) {
	h, store, _ := newTestHandlerWithTrials(t)
	pid := createProfileInStore(t, store, "GPT-Mini")

	rr := postJSON(t, h.HandleCreateTrial, "/api/v1/modeldock/trials", trialPayload(pid))
	if rr.Code != http.StatusCreated {
		t.Errorf("status: want 201, got %d — body: %s", rr.Code, rr.Body)
	}
	var resp map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	trial, ok := resp["trial"].(map[string]any)
	if !ok || trial["id"] == nil {
		t.Error("expected trial object with ID in response")
	}
}

func TestHandleCreateTrial_MissingName(t *testing.T) {
	h, store, _ := newTestHandlerWithTrials(t)
	pid := createProfileInStore(t, store, "GPT-Mini")

	payload := trialPayload(pid)
	payload["name"] = ""

	rr := postJSON(t, h.HandleCreateTrial, "/api/v1/modeldock/trials", payload)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

func TestHandleCreateTrial_NoStore(t *testing.T) {
	h, _, _ := newTestHandlerWithTrials(t)
	h.trialStore = nil
	rr := postJSON(t, h.HandleCreateTrial, "/api/v1/modeldock/trials", map[string]any{"name": "x"})
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", rr.Code)
	}
}

// ── HandleListTrials ────────────────────────────────────────

func TestHandleListTrials_Empty(t *testing.T) {
	h, _, _ := newTestHandlerWithTrials(t)
	rr := getJSON(t, h.HandleListTrials, "/api/v1/modeldock/trials")
	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	trials, ok := resp["trials"].([]any)
	if !ok {
		t.Error("expected trials array")
	}
	if len(trials) != 0 {
		t.Errorf("want 0 trials, got %d", len(trials))
	}
}

func TestHandleListTrials_AfterCreate(t *testing.T) {
	h, store, _ := newTestHandlerWithTrials(t)
	pid := createProfileInStore(t, store, "Model")

	// Create two trials.
	for i := 0; i < 2; i++ {
		rr := postJSON(t, h.HandleCreateTrial, "/api/v1/modeldock/trials", trialPayload(pid))
		if rr.Code != http.StatusCreated {
			t.Fatalf("create trial: %d", rr.Code)
		}
	}

	rr := getJSON(t, h.HandleListTrials, "/api/v1/modeldock/trials")
	var resp map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	trials := resp["trials"].([]any)
	if len(trials) != 2 {
		t.Errorf("want 2 trials, got %d", len(trials))
	}
}

// ── HandleGetTrialResults ───────────────────────────────────

func TestHandleGetTrialResults_NotFound(t *testing.T) {
	h, _, _ := newTestHandlerWithTrials(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/modeldock/trials/nonexistent/results", nil)
	req.SetPathValue("id", "nonexistent")
	rr := httptest.NewRecorder()
	h.HandleGetTrialResults(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestHandleGetTrialResults_NoRuns(t *testing.T) {
	h, store, ts := newTestHandlerWithTrials(t)
	pid := createProfileInStore(t, store, "M")

	// Create trial via store directly.
	trial, _ := ts.CreateTrial(Trial{
		Name:    "T",
		Prompts: []TrialPrompt{{ID: "p1", User: "Hi"}},
		Models:  []TrialModel{{ProfileID: pid}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/modeldock/trials/"+trial.ID+"/results", nil)
	req.SetPathValue("id", trial.ID)
	rr := httptest.NewRecorder()
	h.HandleGetTrialResults(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&resp)
	results := resp["results"].([]any)
	if len(results) != 0 {
		t.Errorf("want 0 results, got %d", len(results))
	}
}

// ── HandleCompareTrialResults ───────────────────────────────

func TestHandleCompareTrialResults_NotFound(t *testing.T) {
	h, _, _ := newTestHandlerWithTrials(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/modeldock/trials/missing/compare", nil)
	req.SetPathValue("id", "missing")
	rr := httptest.NewRecorder()
	h.HandleCompareTrialResults(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestHandleCompareTrialResults_NoRuns(t *testing.T) {
	h, store, ts := newTestHandlerWithTrials(t)
	pid := createProfileInStore(t, store, "M")
	trial, _ := ts.CreateTrial(Trial{
		Name:    "T",
		Prompts: []TrialPrompt{{ID: "p1", User: "Hi"}},
		Models:  []TrialModel{{ProfileID: pid}},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/modeldock/trials/"+trial.ID+"/compare", nil)
	req.SetPathValue("id", trial.ID)
	rr := httptest.NewRecorder()
	h.HandleCompareTrialResults(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rr.Code)
	}
}

// ── HandleRunTrial (with mock execution) ───────────────────

// mockableHandler wraps Handler and overrides buildProviderMap via trialStore injection.
// We use it to test the run flow with a mock provider.
type mockProviderStore struct {
	TrialStore
	profileMap map[string]*Profile
}

// trialRunTestSetup runs a trial end-to-end with a mock LLM provider by directly
// invoking the executor (bypassing the HTTP handler's profile lookup).
func TestTrialRunEndToEnd(t *testing.T) {
	db := newTestTrialDB(t)
	ts, err := NewTrialStore(db)
	if err != nil {
		t.Fatalf("trial store: %v", err)
	}

	trial, _ := ts.CreateTrial(Trial{
		Name: "E2E Trial",
		Prompts: []TrialPrompt{
			{ID: "p1", User: "What is 2+2?", Criteria: EvalCriteria{Type: EvalContains, Expected: "4"}},
		},
		Models: []TrialModel{
			{ProfileID: "mock-model", Label: "Mock"},
		},
		Parameters: TrialParameters{TimeoutSecs: 5},
	})

	// Build executor with a mock client.
	providers := map[string]TrialLLMClient{
		"mock-model": &mockLLMClient{name: "mock", response: "The answer is 4"},
	}
	exec := NewTrialExecutor(providers)
	results, err := exec.Execute(context.Background(), trial)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}

	run, err := ts.CreateRun(trial.ID)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	_ = ts.UpdateRunStatus(run.ID, TrialRunRunning, "")

	for i := range results {
		results[i].RunID = run.ID
		_, _ = ts.SaveResult(results[i])
	}
	_ = ts.UpdateRunStatus(run.ID, TrialRunCompleted, "")

	// Verify stored results.
	stored, err := ts.ListResults(run.ID)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("want 1 stored result, got %d", len(stored))
	}
	if stored[0].QualityScore != 1.0 {
		t.Errorf("quality: want 1.0, got %v", stored[0].QualityScore)
	}
	if stored[0].Response == "" {
		t.Error("response should not be empty")
	}

	// Aggregate and compare.
	aggs, err := ts.AggregateByModel(run.ID)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	report := BuildCompareReport(run.ID, trial.ID, trial, aggs)
	if report.RunID != run.ID {
		t.Errorf("run_id: %s", report.RunID)
	}
	if len(report.Models) != 1 {
		t.Errorf("models: want 1, got %d", len(report.Models))
	}
	if _, ok := report.Rankings["latency"]; !ok {
		t.Error("expected latency ranking")
	}
}

// ── HandleRunTrial — bad profile ───────────────────────────

func TestHandleRunTrial_UnknownProfile(t *testing.T) {
	h, _, ts := newTestHandlerWithTrials(t)

	trial, _ := ts.CreateTrial(Trial{
		Name:    "T",
		Prompts: []TrialPrompt{{ID: "p1", User: "Hi"}},
		Models:  []TrialModel{{ProfileID: "no-such-profile"}},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/modeldock/trials/"+trial.ID+"/run", nil)
	req.SetPathValue("id", trial.ID)
	rr := httptest.NewRecorder()
	h.HandleRunTrial(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for unknown profile, got %d — body: %s", rr.Code, rr.Body)
	}
}

// ── Handler with nil trialStore ─────────────────────────────

func TestHandlersWithNilTrialStore(t *testing.T) {
	h, _, _ := newTestHandlerWithTrials(t)
	h.trialStore = nil

	handlers := []struct {
		name string
		fn   http.HandlerFunc
	}{
		{"list", h.HandleListTrials},
		{"compare", h.HandleCompareTrialResults},
		{"results", h.HandleGetTrialResults},
		{"run", h.HandleRunTrial},
	}

	for _, tc := range handlers {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.SetPathValue("id", "anything")
		rr := httptest.NewRecorder()
		tc.fn(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("[%s] want 503, got %d", tc.name, rr.Code)
		}
	}
}

// ── newHandler auto-creates trialStore ─────────────────────

func TestNewHandlerAutoCreatesTrialStore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "dock.db")
	store, err := NewStore(storePath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	h := NewHandler(store, nil, nil)
	if h.trialStore == nil {
		t.Error("NewHandler should auto-create trialStore when store != nil")
	}
}

// Ensure the existing mock satisfies the TrialLLMClient interface.
var _ TrialLLMClient = (*mockLLMClient)(nil)
var _ TrialLLMClient = llm.NewOpenAIProvider(llm.ProviderConfig{})
