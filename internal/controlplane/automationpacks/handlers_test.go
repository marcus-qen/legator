package automationpacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestHandler(t *testing.T, opts ...HandlerOption) *Handler {
	t.Helper()
	store := newTestStore(t)
	return NewHandler(store, opts...)
}

func newTestHandlerWithStore(t *testing.T, opts ...HandlerOption) (*Handler, *Store) {
	t.Helper()
	store := newTestStore(t)
	return NewHandler(store, opts...), store
}

func TestHandlerCreateListAndGetDefinition(t *testing.T) {
	h := newTestHandler(t)
	def := validDefinitionFixture()
	body, _ := json.Marshal(def)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/automation-packs", bytes.NewReader(body))
	createRR := httptest.NewRecorder()
	h.HandleCreateDefinition(createRR, createReq)
	if createRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", createRR.Code, createRR.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/automation-packs", nil)
	listRR := httptest.NewRecorder()
	h.HandleListDefinitions(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRR.Code)
	}

	var listPayload struct {
		AutomationPacks []DefinitionSummary `json:"automation_packs"`
	}
	if err := json.NewDecoder(listRR.Body).Decode(&listPayload); err != nil {
		t.Fatalf("decode list payload: %v", err)
	}
	if len(listPayload.AutomationPacks) != 1 {
		t.Fatalf("expected 1 pack, got %d", len(listPayload.AutomationPacks))
	}

	pack := listPayload.AutomationPacks[0]
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/automation-packs/"+pack.Metadata.ID+"?version="+pack.Metadata.Version, nil)
	getReq.SetPathValue("id", pack.Metadata.ID)
	getRR := httptest.NewRecorder()
	h.HandleGetDefinition(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", getRR.Code, getRR.Body.String())
	}
}

func TestHandlerCreateDefinitionRejectsInvalidBody(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/automation-packs", bytes.NewBufferString(`{"metadata":`))
	rr := httptest.NewRecorder()
	h.HandleCreateDefinition(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestHandlerCreateDefinitionRejectsInvalidSchema(t *testing.T) {
	h := newTestHandler(t)
	invalid := Definition{Metadata: Metadata{ID: "bad", Version: "1.0.0"}}
	body, _ := json.Marshal(invalid)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/automation-packs", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	h.HandleCreateDefinition(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandlerGetDefinitionNotFound(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/automation-packs/missing", nil)
	req.SetPathValue("id", "missing")
	rr := httptest.NewRecorder()
	h.HandleGetDefinition(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandlerDryRunSimulatesPolicyAndResolvesStepPlan(t *testing.T) {
	simulator := &stubPolicySimulator{
		decisions: map[string]PolicySimulation{
			"prepare": {Outcome: PolicyOutcomeAllow, RiskLevel: "medium", Summary: "safe read-only command"},
			"archive": {Outcome: PolicyOutcomeAllow, RiskLevel: "high", Summary: "requires guardrail review"},
			"cleanup": {Outcome: PolicyOutcomeDeny, RiskLevel: "critical", Summary: "destructive action denied"},
		},
	}
	h := newTestHandler(t, WithPolicySimulator(simulator))

	def := validDefinitionFixture()
	def.Inputs[0].Default = nil
	def.Steps[0].Parameters["command"] = "journalctl -u app --since {{inputs.environment}}"
	def.Steps[1].Parameters["command"] = "systemctl restart api"
	def.Steps = append(def.Steps, Step{
		ID:     "cleanup",
		Action: "run_command",
		Parameters: map[string]any{
			"command": "rm -rf /srv/{{inputs.environment}}",
		},
		ExpectedOutcomes: []ExpectedOutcome{{Description: "cleanup done", SuccessCriteria: "exit_code == 0", Required: true}},
	})

	requestBody, _ := json.Marshal(DryRunRequest{Definition: def, Inputs: map[string]any{"environment": "prod"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/automation-packs/dry-run", bytes.NewReader(requestBody))
	rr := httptest.NewRecorder()

	h.HandleDryRunDefinition(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var payload struct {
		DryRun DryRunResult `json:"dry_run"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode dry-run payload: %v", err)
	}

	if !payload.DryRun.NonMutating {
		t.Fatal("expected dry-run to be non-mutating")
	}
	if got, _ := payload.DryRun.ResolvedInputs["environment"].(string); got != "prod" {
		t.Fatalf("expected resolved input environment=prod, got %#v", payload.DryRun.ResolvedInputs["environment"])
	}
	if len(payload.DryRun.Steps) != 3 {
		t.Fatalf("expected 3 planned steps, got %d", len(payload.DryRun.Steps))
	}
	if !strings.Contains(simulator.commands["prepare"], "journalctl -u app --since prod") {
		t.Fatalf("expected prepare command to include resolved input, got %q", simulator.commands["prepare"])
	}

	if payload.DryRun.Steps[1].PolicySimulation.Outcome != PolicyOutcomeQueue {
		t.Fatalf("expected step-level approval requirement to queue archive step, got %q", payload.DryRun.Steps[1].PolicySimulation.Outcome)
	}
	if payload.DryRun.Steps[2].PolicySimulation.Outcome != PolicyOutcomeDeny {
		t.Fatalf("expected cleanup step deny, got %q", payload.DryRun.Steps[2].PolicySimulation.Outcome)
	}

	if payload.DryRun.WorkflowPolicy.Outcome != PolicyOutcomeDeny {
		t.Fatalf("expected workflow deny roll-up, got %q", payload.DryRun.WorkflowPolicy.Outcome)
	}
	if payload.DryRun.RiskSummary.AllowCount != 1 || payload.DryRun.RiskSummary.QueueCount != 1 || payload.DryRun.RiskSummary.DenyCount != 1 {
		t.Fatalf("unexpected risk summary counts: %+v", payload.DryRun.RiskSummary)
	}
}

func TestHandlerDryRunRejectsInvalidSchema(t *testing.T) {
	h := newTestHandler(t)
	invalid := DryRunRequest{Definition: Definition{Metadata: Metadata{ID: "bad"}}}
	requestBody, _ := json.Marshal(invalid)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/automation-packs/dry-run", bytes.NewReader(requestBody))
	rr := httptest.NewRecorder()

	h.HandleDryRunDefinition(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if code, _ := payload["code"].(string); code != "invalid_schema" {
		t.Fatalf("expected invalid_schema code, got %+v", payload)
	}
}

func TestHandlerDryRunRejectsInvalidInputs(t *testing.T) {
	h := newTestHandler(t)
	def := validDefinitionFixture()
	def.Inputs[0].Default = nil

	requestBody, _ := json.Marshal(DryRunRequest{Definition: def, Inputs: map[string]any{"environment": 42}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/automation-packs/dry-run", bytes.NewReader(requestBody))
	rr := httptest.NewRecorder()

	h.HandleDryRunDefinition(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if code, _ := payload["code"].(string); code != "invalid_inputs" {
		t.Fatalf("expected invalid_inputs code, got %+v", payload)
	}
}

func TestHandlerDryRunDoesNotMutateStore(t *testing.T) {
	h, store := newTestHandlerWithStore(t)
	persisted := validDefinitionFixture()
	if _, err := store.CreateDefinition(persisted); err != nil {
		t.Fatalf("seed definition: %v", err)
	}

	listsBefore, err := store.ListDefinitions()
	if err != nil {
		t.Fatalf("list before: %v", err)
	}

	dryRunDef := validDefinitionFixture()
	dryRunDef.Metadata.ID = "ops.temp-dry-run"
	dryRunDef.Metadata.Version = "2.0.0"
	requestBody, _ := json.Marshal(DryRunRequest{Definition: dryRunDef, Inputs: map[string]any{"environment": "prod"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/automation-packs/dry-run", bytes.NewReader(requestBody))
	rr := httptest.NewRecorder()
	h.HandleDryRunDefinition(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	listsAfter, err := store.ListDefinitions()
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(listsAfter) != len(listsBefore) {
		t.Fatalf("dry-run should not persist definitions: before=%d after=%d", len(listsBefore), len(listsAfter))
	}
	if _, err := store.GetDefinition(dryRunDef.Metadata.ID, dryRunDef.Metadata.Version); !IsNotFound(err) {
		t.Fatalf("expected dry-run definition to remain absent, got err=%v", err)
	}
}

func TestHandlerStartAndGetExecution(t *testing.T) {
	h, store := newTestHandlerWithStore(
		t,
		WithActionRunner(ActionRunnerFunc(func(_ context.Context, _ ActionRequest) (*ActionResult, error) {
			return &ActionResult{Output: map[string]any{"ok": true}}, nil
		})),
		WithPolicySimulator(PolicySimulatorFunc(func(_ context.Context, _ PolicySimulationRequest) PolicySimulation {
			return PolicySimulation{Outcome: PolicyOutcomeAllow, RiskLevel: "low", Summary: "allowed"}
		})),
	)

	def := executionDefinitionFixture("ops.handler.exec", []Step{{ID: "prepare", Action: "run_command", Parameters: map[string]any{"command": "echo hi"}}})
	if _, err := store.CreateDefinition(def); err != nil {
		t.Fatalf("seed definition: %v", err)
	}

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/automation-packs/ops.handler.exec/executions", bytes.NewBufferString(`{"version":"1.0.0"}`))
	startReq.SetPathValue("id", def.Metadata.ID)
	startRR := httptest.NewRecorder()
	h.HandleStartExecution(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", startRR.Code, startRR.Body.String())
	}

	var startPayload struct {
		Execution Execution `json:"execution"`
	}
	if err := json.NewDecoder(startRR.Body).Decode(&startPayload); err != nil {
		t.Fatalf("decode start payload: %v", err)
	}
	if startPayload.Execution.ID == "" {
		t.Fatalf("expected execution id, payload=%+v", startPayload)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/automation-packs/executions/"+startPayload.Execution.ID, nil)
	getReq.SetPathValue("executionID", startPayload.Execution.ID)
	getRR := httptest.NewRecorder()
	h.HandleGetExecution(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", getRR.Code, getRR.Body.String())
	}
}

func TestHandlerGetExecutionTimelineAndArtifacts(t *testing.T) {
	h, store := newTestHandlerWithStore(
		t,
		WithActionRunner(ActionRunnerFunc(func(_ context.Context, req ActionRequest) (*ActionResult, error) {
			if req.StepID == "archive" {
				return nil, errors.New("archive failed")
			}
			return &ActionResult{
				Output:        map[string]any{"ok": true},
				StdoutSnippet: "hello stdout",
				Artifacts:     map[string]any{"exit_code": 0},
			}, nil
		})),
		WithPolicySimulator(PolicySimulatorFunc(func(_ context.Context, _ PolicySimulationRequest) PolicySimulation {
			return PolicySimulation{Outcome: PolicyOutcomeAllow, RiskLevel: "medium", Summary: "allowed", Rationale: map[string]any{"policy": "capacity"}}
		})),
	)

	def := executionDefinitionFixture("ops.handler.timeline", []Step{
		{ID: "prepare", Action: "run_command", Mutating: boolPtr(true), Approval: &ApprovalRequirement{Required: true, MinimumApprovers: 1, ApproverRoles: []string{"ops"}}},
		{ID: "archive", Action: "apply", Mutating: boolPtr(true)},
	})
	if _, err := store.CreateDefinition(def); err != nil {
		t.Fatalf("seed definition: %v", err)
	}

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/automation-packs/ops.handler.timeline/executions", bytes.NewBufferString(`{"version":"1.0.0","approval_context":{"workflow":{"approved":true,"approver_count":1,"approved_by":["ops@example.com"]},"steps":{"prepare":{"approved":true,"approver_count":1,"approved_by":["ops@example.com"]}}}}`))
	startReq.SetPathValue("id", def.Metadata.ID)
	startRR := httptest.NewRecorder()
	h.HandleStartExecution(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", startRR.Code, startRR.Body.String())
	}
	var startPayload struct {
		Execution Execution `json:"execution"`
	}
	if err := json.NewDecoder(startRR.Body).Decode(&startPayload); err != nil {
		t.Fatalf("decode start payload: %v", err)
	}
	if startPayload.Execution.ID == "" {
		t.Fatal("expected execution id")
	}

	timelineReq := httptest.NewRequest(http.MethodGet, "/api/v1/automation-packs/executions/"+startPayload.Execution.ID+"/timeline?step_id=prepare", nil)
	timelineReq.SetPathValue("executionID", startPayload.Execution.ID)
	timelineRR := httptest.NewRecorder()
	h.HandleGetExecutionTimeline(timelineRR, timelineReq)
	if timelineRR.Code != http.StatusOK {
		t.Fatalf("expected 200 timeline, got %d body=%s", timelineRR.Code, timelineRR.Body.String())
	}
	var timelinePayload struct {
		ExecutionID string                   `json:"execution_id"`
		Timeline    []ExecutionTimelineEvent `json:"timeline"`
		Replay      ExecutionReplay          `json:"replay"`
	}
	if err := json.NewDecoder(timelineRR.Body).Decode(&timelinePayload); err != nil {
		t.Fatalf("decode timeline payload: %v", err)
	}
	if timelinePayload.ExecutionID != startPayload.Execution.ID {
		t.Fatalf("unexpected execution id: %q", timelinePayload.ExecutionID)
	}
	if len(timelinePayload.Timeline) == 0 {
		t.Fatal("expected non-empty timeline")
	}
	for _, event := range timelinePayload.Timeline {
		if !strings.EqualFold(event.StepID, "prepare") {
			t.Fatalf("expected timeline filter to keep only prepare step events, got step=%q", event.StepID)
		}
	}
	if !timelinePayload.Replay.Deterministic {
		t.Fatalf("expected deterministic replay payload, got %+v", timelinePayload.Replay)
	}

	artifactsReq := httptest.NewRequest(http.MethodGet, "/api/v1/automation-packs/executions/"+startPayload.Execution.ID+"/artifacts?type=stdout_snippet", nil)
	artifactsReq.SetPathValue("executionID", startPayload.Execution.ID)
	artifactsRR := httptest.NewRecorder()
	h.HandleGetExecutionArtifacts(artifactsRR, artifactsReq)
	if artifactsRR.Code != http.StatusOK {
		t.Fatalf("expected 200 artifacts, got %d body=%s", artifactsRR.Code, artifactsRR.Body.String())
	}
	var artifactsPayload struct {
		ExecutionID string              `json:"execution_id"`
		Artifacts   []ExecutionArtifact `json:"artifacts"`
	}
	if err := json.NewDecoder(artifactsRR.Body).Decode(&artifactsPayload); err != nil {
		t.Fatalf("decode artifacts payload: %v", err)
	}
	if artifactsPayload.ExecutionID != startPayload.Execution.ID {
		t.Fatalf("unexpected execution id in artifacts payload: %q", artifactsPayload.ExecutionID)
	}
	if len(artifactsPayload.Artifacts) == 0 {
		t.Fatal("expected filtered artifacts")
	}
	for _, artifact := range artifactsPayload.Artifacts {
		if artifact.Type != ArtifactTypeStdoutSnippet {
			t.Fatalf("expected artifact filter to keep only stdout_snippet, got %q", artifact.Type)
		}
	}
}

func TestHandlerExecutionTimelineAndArtifactsErrors(t *testing.T) {
	h := newTestHandler(t)

	missingTimelineReq := httptest.NewRequest(http.MethodGet, "/api/v1/automation-packs/executions//timeline", nil)
	missingTimelineReq.SetPathValue("executionID", "")
	missingTimelineRR := httptest.NewRecorder()
	h.HandleGetExecutionTimeline(missingTimelineRR, missingTimelineReq)
	if missingTimelineRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing timeline execution id, got %d", missingTimelineRR.Code)
	}

	notFoundTimelineReq := httptest.NewRequest(http.MethodGet, "/api/v1/automation-packs/executions/missing/timeline", nil)
	notFoundTimelineReq.SetPathValue("executionID", "missing")
	notFoundTimelineRR := httptest.NewRecorder()
	h.HandleGetExecutionTimeline(notFoundTimelineRR, notFoundTimelineReq)
	if notFoundTimelineRR.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing timeline execution, got %d body=%s", notFoundTimelineRR.Code, notFoundTimelineRR.Body.String())
	}

	notFoundArtifactsReq := httptest.NewRequest(http.MethodGet, "/api/v1/automation-packs/executions/missing/artifacts", nil)
	notFoundArtifactsReq.SetPathValue("executionID", "missing")
	notFoundArtifactsRR := httptest.NewRecorder()
	h.HandleGetExecutionArtifacts(notFoundArtifactsRR, notFoundArtifactsReq)
	if notFoundArtifactsRR.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing artifacts execution, got %d body=%s", notFoundArtifactsRR.Code, notFoundArtifactsRR.Body.String())
	}
}

func TestHandlerStartExecutionDefinitionNotFound(t *testing.T) {
	h := newTestHandler(t)
	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/automation-packs/missing/executions", bytes.NewBufferString(`{"version":"1.0.0"}`))
	startReq.SetPathValue("id", "missing")
	startRR := httptest.NewRecorder()
	h.HandleStartExecution(startRR, startReq)
	if startRR.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", startRR.Code, startRR.Body.String())
	}
}

type stubPolicySimulator struct {
	decisions map[string]PolicySimulation
	commands  map[string]string
}

func (s *stubPolicySimulator) Simulate(_ context.Context, req PolicySimulationRequest) PolicySimulation {
	if s.commands == nil {
		s.commands = make(map[string]string)
	}
	s.commands[req.Step.ID] = req.Command.Command
	if decision, ok := s.decisions[req.Step.ID]; ok {
		return decision
	}
	return PolicySimulation{Outcome: PolicyOutcomeAllow, RiskLevel: "low", Summary: "default allow"}
}
