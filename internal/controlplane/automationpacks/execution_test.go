package automationpacks

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type scriptedOutcome struct {
	err    error
	output map[string]any
}

type actionCall struct {
	StepID   string
	Action   string
	Attempt  int
	Rollback bool
}

type scriptedActionRunner struct {
	mu       sync.Mutex
	outcomes map[string][]scriptedOutcome
	calls    []actionCall
}

func (r *scriptedActionRunner) Run(_ context.Context, req ActionRequest) (*ActionResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := req.StepID
	if req.Rollback {
		key = key + "#rollback"
	}
	r.calls = append(r.calls, actionCall{StepID: req.StepID, Action: req.Action, Attempt: req.Attempt, Rollback: req.Rollback})

	queue := r.outcomes[key]
	if len(queue) == 0 {
		return &ActionResult{Output: map[string]any{"ok": true}}, nil
	}
	outcome := queue[0]
	r.outcomes[key] = queue[1:]
	if outcome.err != nil {
		return nil, outcome.err
	}
	return &ActionResult{Output: cloneMap(outcome.output)}, nil
}

func (r *scriptedActionRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *scriptedActionRunner) callSnapshot() []actionCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]actionCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func TestExecutionRuntimeSuccessfulFlow(t *testing.T) {
	store := newTestStore(t)
	def := executionDefinitionFixture("ops.exec.success", []Step{
		{ID: "prepare", Action: "run_command", Parameters: map[string]any{"command": "echo prepare"}},
		{ID: "archive", Action: "upload_artifact", Parameters: map[string]any{"bucket": "ops"}},
	})
	if _, err := store.CreateDefinition(def); err != nil {
		t.Fatalf("create definition: %v", err)
	}

	runner := &scriptedActionRunner{outcomes: map[string][]scriptedOutcome{}}
	runtime := NewExecutionRuntime(store, PolicySimulatorFunc(func(_ context.Context, _ PolicySimulationRequest) PolicySimulation {
		return PolicySimulation{Outcome: PolicyOutcomeAllow, RiskLevel: "low", Summary: "allowed"}
	}), runner)

	exec, err := runtime.Start(context.Background(), StartExecutionRequest{DefinitionID: def.Metadata.ID, Version: def.Metadata.Version})
	if err != nil {
		t.Fatalf("start execution: %v", err)
	}
	if exec.Status != ExecutionStatusSucceeded {
		t.Fatalf("expected succeeded execution, got %q", exec.Status)
	}
	if len(exec.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(exec.Steps))
	}
	for _, step := range exec.Steps {
		if step.Status != StepStatusSucceeded {
			t.Fatalf("expected step %s to succeed, got %q", step.ID, step.Status)
		}
		if step.Attempts != 1 {
			t.Fatalf("expected one attempt for step %s, got %d", step.ID, step.Attempts)
		}
	}
	if runner.callCount() != 2 {
		t.Fatalf("expected 2 action calls, got %d", runner.callCount())
	}
}

func TestExecutionRuntimeTimeoutRetryHandling(t *testing.T) {
	store := newTestStore(t)
	def := executionDefinitionFixture("ops.exec.retry", []Step{
		{ID: "unstable", Action: "run_command", TimeoutSeconds: 1, MaxRetries: 2, Parameters: map[string]any{"command": "unstable-command"}},
	})
	if _, err := store.CreateDefinition(def); err != nil {
		t.Fatalf("create definition: %v", err)
	}

	runner := &scriptedActionRunner{outcomes: map[string][]scriptedOutcome{
		"unstable": {
			{err: context.DeadlineExceeded},
			{err: context.DeadlineExceeded},
			{output: map[string]any{"attempt": 3}},
		},
	}}
	runtime := NewExecutionRuntime(store, PolicySimulatorFunc(func(_ context.Context, _ PolicySimulationRequest) PolicySimulation {
		return PolicySimulation{Outcome: PolicyOutcomeAllow, RiskLevel: "medium", Summary: "allowed"}
	}), runner)

	exec, err := runtime.Start(context.Background(), StartExecutionRequest{DefinitionID: def.Metadata.ID, Version: def.Metadata.Version})
	if err != nil {
		t.Fatalf("start execution: %v", err)
	}
	if exec.Status != ExecutionStatusSucceeded {
		t.Fatalf("expected succeeded execution after retries, got %q", exec.Status)
	}
	step := exec.Steps[0]
	if step.Attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", step.Attempts)
	}
	if len(step.AttemptHistory) != 3 {
		t.Fatalf("expected 3 attempt history entries, got %d", len(step.AttemptHistory))
	}
	if step.AttemptHistory[0].Status != StepStatusTimedOut || step.AttemptHistory[1].Status != StepStatusTimedOut || step.AttemptHistory[2].Status != StepStatusSucceeded {
		t.Fatalf("unexpected attempt history statuses: %+v", step.AttemptHistory)
	}
}

func TestExecutionRuntimeRollbackOnFailure(t *testing.T) {
	store := newTestStore(t)
	def := executionDefinitionFixture("ops.exec.rollback", []Step{
		{ID: "step-1", Action: "apply", Rollback: &RollbackHook{Action: "rollback-1"}},
		{ID: "step-2", Action: "apply", Rollback: &RollbackHook{Action: "rollback-2"}},
		{ID: "step-3", Action: "apply"},
	})
	if _, err := store.CreateDefinition(def); err != nil {
		t.Fatalf("create definition: %v", err)
	}

	runner := &scriptedActionRunner{outcomes: map[string][]scriptedOutcome{
		"step-3":          {{err: errors.New("boom")}},
		"step-2#rollback": {{output: map[string]any{"rolled_back": "step-2"}}},
		"step-1#rollback": {{output: map[string]any{"rolled_back": "step-1"}}},
	}}
	runtime := NewExecutionRuntime(store, PolicySimulatorFunc(func(_ context.Context, _ PolicySimulationRequest) PolicySimulation {
		return PolicySimulation{Outcome: PolicyOutcomeAllow, RiskLevel: "high", Summary: "allowed"}
	}), runner)

	exec, err := runtime.Start(context.Background(), StartExecutionRequest{DefinitionID: def.Metadata.ID, Version: def.Metadata.Version})
	if err != nil {
		t.Fatalf("start execution: %v", err)
	}
	if exec.Status != ExecutionStatusFailed {
		t.Fatalf("expected failed execution, got %q", exec.Status)
	}
	if exec.RollbackStatus != RollbackStatusCompleted {
		t.Fatalf("expected rollback status completed, got %q", exec.RollbackStatus)
	}
	if len(exec.Rollback) != 2 {
		t.Fatalf("expected 2 rollback results, got %d", len(exec.Rollback))
	}
	if exec.Rollback[0].StepID != "step-2" || exec.Rollback[1].StepID != "step-1" {
		t.Fatalf("unexpected rollback order: %+v", exec.Rollback)
	}
	if exec.Rollback[0].Status != StepStatusSucceeded || exec.Rollback[1].Status != StepStatusSucceeded {
		t.Fatalf("expected rollback steps to succeed: %+v", exec.Rollback)
	}

	calls := runner.callSnapshot()
	if len(calls) != 5 {
		t.Fatalf("expected 5 total calls (3 forward + 2 rollback), got %d", len(calls))
	}
	if !calls[3].Rollback || calls[3].StepID != "step-2" || !calls[4].Rollback || calls[4].StepID != "step-1" {
		t.Fatalf("unexpected rollback call sequence: %+v", calls)
	}
}

func TestExecutionRuntimePolicyAndApprovalBlocking(t *testing.T) {
	t.Run("policy blocking", func(t *testing.T) {
		store := newTestStore(t)
		mutating := true
		def := executionDefinitionFixture("ops.exec.policy-block", []Step{
			{ID: "guarded", Action: "apply", Mutating: &mutating},
		})
		if _, err := store.CreateDefinition(def); err != nil {
			t.Fatalf("create definition: %v", err)
		}

		runner := &scriptedActionRunner{outcomes: map[string][]scriptedOutcome{}}
		runtime := NewExecutionRuntime(store, PolicySimulatorFunc(func(_ context.Context, req PolicySimulationRequest) PolicySimulation {
			if req.Step.ID == "guarded" {
				return PolicySimulation{Outcome: PolicyOutcomeDeny, RiskLevel: "critical", Summary: "blocked by policy"}
			}
			return PolicySimulation{Outcome: PolicyOutcomeAllow}
		}), runner)

		exec, err := runtime.Start(context.Background(), StartExecutionRequest{DefinitionID: def.Metadata.ID, Version: def.Metadata.Version})
		if err != nil {
			t.Fatalf("start execution: %v", err)
		}
		if exec.Status != ExecutionStatusBlocked {
			t.Fatalf("expected blocked execution, got %q", exec.Status)
		}
		if exec.Failure == nil || exec.Failure.Category != "policy" {
			t.Fatalf("expected policy failure details, got %+v", exec.Failure)
		}
		if runner.callCount() != 0 {
			t.Fatalf("expected no action calls when policy blocks, got %d", runner.callCount())
		}
	})

	t.Run("approval blocking", func(t *testing.T) {
		store := newTestStore(t)
		mutating := true
		def := executionDefinitionFixture("ops.exec.approval-block", []Step{
			{
				ID:       "guarded",
				Action:   "apply",
				Mutating: &mutating,
				Approval: &ApprovalRequirement{Required: true, MinimumApprovers: 1, ApproverRoles: []string{"ops"}},
			},
		})
		if _, err := store.CreateDefinition(def); err != nil {
			t.Fatalf("create definition: %v", err)
		}

		runner := &scriptedActionRunner{outcomes: map[string][]scriptedOutcome{}}
		runtime := NewExecutionRuntime(store, PolicySimulatorFunc(func(_ context.Context, _ PolicySimulationRequest) PolicySimulation {
			return PolicySimulation{Outcome: PolicyOutcomeAllow, RiskLevel: "high", Summary: "allowed"}
		}), runner)

		exec, err := runtime.Start(context.Background(), StartExecutionRequest{DefinitionID: def.Metadata.ID, Version: def.Metadata.Version})
		if err != nil {
			t.Fatalf("start execution: %v", err)
		}
		if exec.Status != ExecutionStatusBlocked {
			t.Fatalf("expected blocked execution, got %q", exec.Status)
		}
		if exec.Failure == nil || exec.Failure.Category != "approval" {
			t.Fatalf("expected approval failure details, got %+v", exec.Failure)
		}
		if runner.callCount() != 0 {
			t.Fatalf("expected no action calls when approval is missing, got %d", runner.callCount())
		}
	})
}

func executionDefinitionFixture(id string, steps []Step) Definition {
	return Definition{
		Metadata: Metadata{
			ID:      id,
			Name:    "Execution Fixture",
			Version: "1.0.0",
		},
		Steps: steps,
		ExpectedOutcomes: []ExpectedOutcome{
			{Description: "workflow complete", SuccessCriteria: "all steps succeeded", Required: true},
		},
	}
}
