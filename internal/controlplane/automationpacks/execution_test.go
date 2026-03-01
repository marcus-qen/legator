package automationpacks

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type scriptedOutcome struct {
	err       error
	output    map[string]any
	message   string
	stdout    string
	stderr    string
	artifacts map[string]any
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
	return &ActionResult{
		Output:        cloneMap(outcome.output),
		Message:       outcome.message,
		StdoutSnippet: outcome.stdout,
		StderrSnippet: outcome.stderr,
		Artifacts:     cloneMap(outcome.artifacts),
	}, nil
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

func TestExecutionRuntimePersistsOrderedTimelineAndArtifacts(t *testing.T) {
	store := newTestStore(t)
	def := executionDefinitionFixture("ops.exec.timeline", []Step{
		{
			ID:       "mutating-step",
			Action:   "run_command",
			Mutating: boolPtr(true),
			Parameters: map[string]any{
				"command": "systemctl restart api",
			},
			Approval: &ApprovalRequirement{Required: true, MinimumApprovers: 1, ApproverRoles: []string{"ops"}},
		},
		{
			ID:       "failing-step",
			Action:   "apply",
			Mutating: boolPtr(true),
		},
	})
	if _, err := store.CreateDefinition(def); err != nil {
		t.Fatalf("create definition: %v", err)
	}

	runner := &scriptedActionRunner{outcomes: map[string][]scriptedOutcome{
		"mutating-step": {
			{
				output:    map[string]any{"ok": true},
				message:   "step one completed",
				stdout:    "stdout line 1\nstdout line 2",
				stderr:    "stderr note",
				artifacts: map[string]any{"exit_code": 0, "host": "node-1"},
			},
		},
		"failing-step": {
			{err: errors.New("forced failure")},
		},
	}}
	runtime := NewExecutionRuntime(store, PolicySimulatorFunc(func(_ context.Context, req PolicySimulationRequest) PolicySimulation {
		return PolicySimulation{
			Outcome:   PolicyOutcomeAllow,
			RiskLevel: "high",
			Summary:   "guardrails satisfied",
			Rationale: map[string]any{"policy": "capacity-policy-v1", "drove_outcome": true},
		}
	}), runner)

	execResult, err := runtime.Start(context.Background(), StartExecutionRequest{
		DefinitionID: def.Metadata.ID,
		Version:      def.Metadata.Version,
		ApprovalContext: ExecutionApprovalContext{
			Workflow: ApprovalDecision{Approved: true, ApproverCount: 1, ApprovedBy: []string{"ops@example.com"}},
			Steps: map[string]ApprovalDecision{
				"mutating-step": {Approved: true, ApproverCount: 1, ApprovedBy: []string{"ops@example.com"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("start execution: %v", err)
	}
	if execResult.Status != ExecutionStatusFailed {
		t.Fatalf("expected failed execution, got %q", execResult.Status)
	}

	timeline, err := runtime.GetTimeline(execResult.ID)
	if err != nil {
		t.Fatalf("get timeline: %v", err)
	}
	if len(timeline) == 0 {
		t.Fatal("expected timeline events")
	}
	seenIDs := map[string]struct{}{}
	for idx, event := range timeline {
		wantSeq := idx + 1
		if event.Sequence != wantSeq {
			t.Fatalf("unexpected sequence at idx %d: got=%d want=%d", idx, event.Sequence, wantSeq)
		}
		if event.ID == "" {
			t.Fatalf("event %d missing id", idx)
		}
		if _, dup := seenIDs[event.ID]; dup {
			t.Fatalf("duplicate event id %q", event.ID)
		}
		seenIDs[event.ID] = struct{}{}
		if event.Timestamp.IsZero() {
			t.Fatalf("event %d missing timestamp", idx)
		}
	}
	if timeline[0].Type != TimelineEventExecutionStarted {
		t.Fatalf("expected first event %q, got %q", TimelineEventExecutionStarted, timeline[0].Type)
	}
	if timeline[len(timeline)-1].Type != TimelineEventExecutionFinished {
		t.Fatalf("expected last event %q, got %q", TimelineEventExecutionFinished, timeline[len(timeline)-1].Type)
	}

	artifacts, err := runtime.GetArtifacts(execResult.ID)
	if err != nil {
		t.Fatalf("get artifacts: %v", err)
	}
	if len(artifacts) == 0 {
		t.Fatal("expected execution artifacts")
	}
	foundTypes := map[string]bool{}
	for _, artifact := range artifacts {
		foundTypes[artifact.Type] = true
	}
	for _, required := range []string{ArtifactTypePolicyRationale, ArtifactTypeApproval, ArtifactTypeStdoutSnippet, ArtifactTypeStderrSnippet, ArtifactTypeActionPayload, ArtifactTypeErrorContext} {
		if !foundTypes[required] {
			t.Fatalf("missing artifact type %q, got=%v", required, foundTypes)
		}
	}

	replay, err := runtime.GetReplay(execResult.ID)
	if err != nil {
		t.Fatalf("get replay: %v", err)
	}
	if replay == nil || !replay.Deterministic {
		t.Fatalf("expected deterministic replay payload, got %+v", replay)
	}
	if replay.EventCount != len(timeline) {
		t.Fatalf("expected replay event_count %d, got %d", len(timeline), replay.EventCount)
	}
	if len(replay.OrderedEventIDs) != len(timeline) {
		t.Fatalf("expected ordered_event_ids length %d, got %d", len(timeline), len(replay.OrderedEventIDs))
	}
	for idx, eventID := range replay.OrderedEventIDs {
		if eventID != timeline[idx].ID {
			t.Fatalf("replay ordering mismatch at idx %d: got=%q want=%q", idx, eventID, timeline[idx].ID)
		}
	}
}

func TestExecutionRuntimeApprovalBlockPersistsCheckpointArtifacts(t *testing.T) {
	store := newTestStore(t)
	def := executionDefinitionFixture("ops.exec.approval-audit", []Step{{
		ID:       "guarded",
		Action:   "apply",
		Mutating: boolPtr(true),
		Approval: &ApprovalRequirement{Required: true, MinimumApprovers: 2, ApproverRoles: []string{"ops", "security"}},
	}})
	if _, err := store.CreateDefinition(def); err != nil {
		t.Fatalf("create definition: %v", err)
	}

	runtime := NewExecutionRuntime(store, PolicySimulatorFunc(func(_ context.Context, _ PolicySimulationRequest) PolicySimulation {
		return PolicySimulation{Outcome: PolicyOutcomeAllow, RiskLevel: "high", Summary: "allowed"}
	}), &scriptedActionRunner{outcomes: map[string][]scriptedOutcome{}})

	execResult, err := runtime.Start(context.Background(), StartExecutionRequest{
		DefinitionID: def.Metadata.ID,
		Version:      def.Metadata.Version,
		ApprovalContext: ExecutionApprovalContext{
			Steps: map[string]ApprovalDecision{
				"guarded": {Approved: true, ApproverCount: 1, ApprovedBy: []string{"ops@example.com"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("start execution: %v", err)
	}
	if execResult.Status != ExecutionStatusBlocked {
		t.Fatalf("expected blocked execution, got %q", execResult.Status)
	}

	timeline, err := runtime.GetTimeline(execResult.ID)
	if err != nil {
		t.Fatalf("get timeline: %v", err)
	}
	foundCheckpoint := false
	for _, event := range timeline {
		if event.Type == TimelineEventStepApprovalCheck {
			foundCheckpoint = true
			break
		}
	}
	if !foundCheckpoint {
		t.Fatal("expected approval checkpoint event in timeline")
	}

	artifacts, err := runtime.GetArtifacts(execResult.ID)
	if err != nil {
		t.Fatalf("get artifacts: %v", err)
	}
	foundApprovalArtifact := false
	for _, artifact := range artifacts {
		if artifact.Type == ArtifactTypeApproval {
			foundApprovalArtifact = true
			break
		}
	}
	if !foundApprovalArtifact {
		t.Fatal("expected approval checkpoint artifact")
	}
}

func boolPtr(v bool) *bool {
	return &v
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
