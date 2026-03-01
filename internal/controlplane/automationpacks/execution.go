package automationpacks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
)

const (
	ExecutionStatusPending   = "pending"
	ExecutionStatusRunning   = "running"
	ExecutionStatusSucceeded = "succeeded"
	ExecutionStatusFailed    = "failed"
	ExecutionStatusBlocked   = "blocked"

	StepStatusPending   = "pending"
	StepStatusRunning   = "running"
	StepStatusSucceeded = "succeeded"
	StepStatusFailed    = "failed"
	StepStatusTimedOut  = "timed_out"
	StepStatusBlocked   = "blocked"
	StepStatusSkipped   = "skipped"

	RollbackStatusNotRequired = "not_required"
	RollbackStatusCompleted   = "completed"
	RollbackStatusPartial     = "partial"
)

var (
	ErrExecutionNotFound = errors.New("automation pack execution not found")
)

type definitionReader interface {
	GetDefinition(id, version string) (*Definition, error)
}

// StartExecutionRequest starts one workflow execution from a stored definition.
type StartExecutionRequest struct {
	DefinitionID    string                   `json:"-"`
	Version         string                   `json:"version,omitempty"`
	Inputs          map[string]any           `json:"inputs,omitempty"`
	ApprovalContext ExecutionApprovalContext `json:"approval_context,omitempty"`
}

// ExecutionApprovalContext carries additive, operator-provided approval decisions.
type ExecutionApprovalContext struct {
	Workflow ApprovalDecision            `json:"workflow,omitempty"`
	Steps    map[string]ApprovalDecision `json:"steps,omitempty"`
}

// ApprovalDecision captures additive approval state supplied at execution time.
type ApprovalDecision struct {
	Approved      bool     `json:"approved"`
	ApproverCount int      `json:"approver_count,omitempty"`
	ApprovedBy    []string `json:"approved_by,omitempty"`
	Note          string   `json:"note,omitempty"`
}

func (c ExecutionApprovalContext) stepDecision(stepID string) ApprovalDecision {
	if len(c.Steps) == 0 {
		return ApprovalDecision{}
	}
	decision, ok := c.Steps[strings.TrimSpace(stepID)]
	if !ok {
		return ApprovalDecision{}
	}
	return decision
}

// Execution captures workflow-level status plus per-step state.
type Execution struct {
	ID             string                  `json:"id"`
	Metadata       Metadata                `json:"metadata"`
	Status         string                  `json:"status"`
	StartedAt      time.Time               `json:"started_at"`
	FinishedAt     *time.Time              `json:"finished_at,omitempty"`
	ResolvedInputs map[string]any          `json:"resolved_inputs,omitempty"`
	Steps          []ExecutionStep         `json:"steps"`
	Failure        *ExecutionFailure       `json:"failure,omitempty"`
	RollbackStatus string                  `json:"rollback_status,omitempty"`
	Rollback       []RollbackExecutionStep `json:"rollback,omitempty"`
}

// ExecutionFailure captures the first blocking/failing step and reason.
type ExecutionFailure struct {
	StepID   string `json:"step_id,omitempty"`
	Category string `json:"category"`
	Message  string `json:"message"`
}

// ExecutionStep is one tracked step within an execution.
type ExecutionStep struct {
	Order              int                    `json:"order"`
	ID                 string                 `json:"id"`
	Name               string                 `json:"name,omitempty"`
	Action             string                 `json:"action"`
	Mutating           bool                   `json:"mutating"`
	Status             string                 `json:"status"`
	Attempts           int                    `json:"attempts"`
	MaxRetries         int                    `json:"max_retries"`
	TimeoutSeconds     int                    `json:"timeout_seconds"`
	StartedAt          *time.Time             `json:"started_at,omitempty"`
	FinishedAt         *time.Time             `json:"finished_at,omitempty"`
	Error              string                 `json:"error,omitempty"`
	ResolvedParameters map[string]any         `json:"resolved_parameters,omitempty"`
	PolicySimulation   PolicySimulation       `json:"policy_simulation,omitempty"`
	AttemptHistory     []ExecutionStepAttempt `json:"attempt_history,omitempty"`
	Output             map[string]any         `json:"output,omitempty"`
	Rollback           *RollbackExecutionStep `json:"rollback,omitempty"`
}

// ExecutionStepAttempt tracks one attempt under timeout/retry controls.
type ExecutionStepAttempt struct {
	Attempt    int        `json:"attempt"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// RollbackExecutionStep records one rollback callback/action result.
type RollbackExecutionStep struct {
	StepID     string         `json:"step_id"`
	Action     string         `json:"action"`
	Status     string         `json:"status"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	Error      string         `json:"error,omitempty"`
	Output     map[string]any `json:"output,omitempty"`
}

// ActionRequest is one executor invocation for a step or rollback hook.
type ActionRequest struct {
	Definition  Metadata       `json:"definition"`
	ExecutionID string         `json:"execution_id"`
	StepID      string         `json:"step_id"`
	Action      string         `json:"action"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Inputs      map[string]any `json:"inputs,omitempty"`
	Attempt     int            `json:"attempt"`
	Rollback    bool           `json:"rollback"`
}

// ActionResult is additive per-step output captured from an executor.
type ActionResult struct {
	Output  map[string]any `json:"output,omitempty"`
	Message string         `json:"message,omitempty"`
}

// ActionRunner executes one concrete automation step action.
type ActionRunner interface {
	Run(ctx context.Context, req ActionRequest) (*ActionResult, error)
}

// ActionRunnerFunc adapts function callbacks to ActionRunner.
type ActionRunnerFunc func(ctx context.Context, req ActionRequest) (*ActionResult, error)

func (fn ActionRunnerFunc) Run(ctx context.Context, req ActionRequest) (*ActionResult, error) {
	if fn == nil {
		return noopActionRunner{}.Run(ctx, req)
	}
	return fn(ctx, req)
}

type noopActionRunner struct{}

func (noopActionRunner) Run(_ context.Context, req ActionRequest) (*ActionResult, error) {
	if strings.EqualFold(strings.TrimSpace(req.Action), "noop") {
		return &ActionResult{Message: "noop action completed"}, nil
	}
	return nil, fmt.Errorf("no action runner configured for action %q", req.Action)
}

// ExecutionRuntime runs automation packs from stored definitions with guardrails.
type ExecutionRuntime struct {
	store              definitionReader
	policySimulator    PolicySimulator
	actionRunner       ActionRunner
	defaultStepTimeout time.Duration
	now                func() time.Time

	mu         sync.RWMutex
	executions map[string]*Execution
	sequence   uint64
}

func NewExecutionRuntime(store definitionReader, simulator PolicySimulator, runner ActionRunner) *ExecutionRuntime {
	if simulator == nil {
		simulator = noopPolicySimulator{}
	}
	if runner == nil {
		runner = noopActionRunner{}
	}
	return &ExecutionRuntime{
		store:              store,
		policySimulator:    simulator,
		actionRunner:       runner,
		defaultStepTimeout: 30 * time.Second,
		now: func() time.Time {
			return time.Now().UTC()
		},
		executions: make(map[string]*Execution),
	}
}

func (r *ExecutionRuntime) Start(ctx context.Context, req StartExecutionRequest) (*Execution, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("execution runtime unavailable")
	}

	definitionID := strings.TrimSpace(strings.ToLower(req.DefinitionID))
	if definitionID == "" {
		return nil, &ValidationError{Issues: []string{"definition id is required"}}
	}

	def, err := r.store.GetDefinition(definitionID, strings.TrimSpace(req.Version))
	if err != nil {
		return nil, err
	}

	resolvedInputs, err := resolveInputs(def.Inputs, req.Inputs)
	if err != nil {
		return nil, err
	}

	now := r.now()
	exec := &Execution{
		ID:             r.nextExecutionID(),
		Metadata:       def.Metadata,
		Status:         ExecutionStatusRunning,
		StartedAt:      now,
		ResolvedInputs: cloneMap(resolvedInputs),
		Steps:          make([]ExecutionStep, len(def.Steps)),
		RollbackStatus: RollbackStatusNotRequired,
	}

	for idx := range def.Steps {
		step := def.Steps[idx]
		exec.Steps[idx] = ExecutionStep{
			Order:          idx + 1,
			ID:             step.ID,
			Name:           step.Name,
			Action:         step.Action,
			Status:         StepStatusPending,
			MaxRetries:     normalizeRetryCount(step.MaxRetries),
			TimeoutSeconds: timeoutSeconds(step.TimeoutSeconds, r.defaultStepTimeout),
		}
	}

	succeededIndexes := make([]int, 0, len(def.Steps))

	for idx := range def.Steps {
		stepDef := def.Steps[idx]
		step := &exec.Steps[idx]
		resolvedParams := resolveStepParameters(stepDef.Parameters, resolvedInputs)
		step.ResolvedParameters = cloneMap(resolvedParams)
		step.Mutating = inferStepMutating(stepDef, resolvedParams, idx)

		stepStarted := r.now()
		step.StartedAt = &stepStarted
		step.Status = StepStatusRunning

		if step.Mutating {
			policy := r.policySimulator.Simulate(ctx, PolicySimulationRequest{
				Definition: def.Metadata,
				Step:       stepDef,
				Command:    commandPayloadForStep(stepDef, resolvedParams, idx),
			})
			if policy.Outcome == "" {
				policy.Outcome = PolicyOutcomeAllow
			}
			if policy.RiskLevel == "" {
				command := commandPayloadForStep(stepDef, resolvedParams, idx)
				policy.RiskLevel = approval.ClassifyRisk(&command)
			}
			policy.Outcome = strings.ToLower(strings.TrimSpace(policy.Outcome))
			step.PolicySimulation = policy

			switch policy.Outcome {
			case PolicyOutcomeDeny:
				r.blockExecution(exec, idx, "policy", fmt.Sprintf("step %s denied by policy: %s", stepDef.ID, strings.TrimSpace(policy.Summary)), succeededIndexes, *def, resolvedInputs)
				return r.persistAndClone(exec), nil
			case PolicyOutcomeQueue:
				r.blockExecution(exec, idx, "policy", fmt.Sprintf("step %s requires approval by policy gate", stepDef.ID), succeededIndexes, *def, resolvedInputs)
				return r.persistAndClone(exec), nil
			}

			if reason := unmetApprovalReason(def.Approval, req.ApprovalContext.Workflow, "workflow"); reason != "" {
				r.blockExecution(exec, idx, "approval", reason, succeededIndexes, *def, resolvedInputs)
				return r.persistAndClone(exec), nil
			}
			if reason := unmetApprovalReason(stepDef.Approval, req.ApprovalContext.stepDecision(stepDef.ID), fmt.Sprintf("step %s", stepDef.ID)); reason != "" {
				r.blockExecution(exec, idx, "approval", reason, succeededIndexes, *def, resolvedInputs)
				return r.persistAndClone(exec), nil
			}
		}

		maxAttempts := step.MaxRetries + 1
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			attemptStarted := r.now()
			attemptState := ExecutionStepAttempt{Attempt: attempt, Status: StepStatusRunning, StartedAt: attemptStarted}

			attemptCtx, cancel := context.WithTimeout(ctx, stepTimeout(stepDef.TimeoutSeconds, r.defaultStepTimeout))
			result, runErr := r.actionRunner.Run(attemptCtx, ActionRequest{
				Definition:  def.Metadata,
				ExecutionID: exec.ID,
				StepID:      stepDef.ID,
				Action:      stepDef.Action,
				Parameters:  resolvedParams,
				Inputs:      resolvedInputs,
				Attempt:     attempt,
				Rollback:    false,
			})
			cancel()

			step.Attempts = attempt
			attemptFinished := r.now()
			attemptState.FinishedAt = &attemptFinished

			if runErr == nil {
				attemptState.Status = StepStatusSucceeded
				step.Status = StepStatusSucceeded
				if result != nil {
					step.Output = cloneMap(result.Output)
				}
				step.AttemptHistory = append(step.AttemptHistory, attemptState)
				stepFinished := r.now()
				step.FinishedAt = &stepFinished
				succeededIndexes = append(succeededIndexes, idx)
				break
			}

			attemptState.Error = runErr.Error()
			if isTimeoutError(runErr) {
				attemptState.Status = StepStatusTimedOut
			} else {
				attemptState.Status = StepStatusFailed
			}
			step.AttemptHistory = append(step.AttemptHistory, attemptState)
			step.Error = runErr.Error()

			if attempt == maxAttempts {
				if isTimeoutError(runErr) {
					step.Status = StepStatusTimedOut
				} else {
					step.Status = StepStatusFailed
				}
				stepFinished := r.now()
				step.FinishedAt = &stepFinished
				r.failExecution(exec, idx, fmt.Sprintf("step %s failed: %s", stepDef.ID, runErr.Error()), succeededIndexes, *def, resolvedInputs)
				return r.persistAndClone(exec), nil
			}
		}
	}

	exec.Status = ExecutionStatusSucceeded
	finished := r.now()
	exec.FinishedAt = &finished
	exec.RollbackStatus = RollbackStatusNotRequired
	return r.persistAndClone(exec), nil
}

func (r *ExecutionRuntime) Get(executionID string) (*Execution, error) {
	if r == nil {
		return nil, ErrExecutionNotFound
	}
	executionID = strings.TrimSpace(executionID)
	if executionID == "" {
		return nil, ErrExecutionNotFound
	}

	r.mu.RLock()
	stored, ok := r.executions[executionID]
	r.mu.RUnlock()
	if !ok {
		return nil, ErrExecutionNotFound
	}
	return cloneExecution(stored), nil
}

func (r *ExecutionRuntime) failExecution(exec *Execution, stepIdx int, message string, succeededIndexes []int, def Definition, resolvedInputs map[string]any) {
	if exec == nil {
		return
	}
	exec.Status = ExecutionStatusFailed
	exec.Failure = &ExecutionFailure{
		StepID:   def.Steps[stepIdx].ID,
		Category: "execution",
		Message:  strings.TrimSpace(message),
	}
	r.markRemainingSkipped(exec, stepIdx)
	r.runRollbackChain(exec, succeededIndexes, def, resolvedInputs)
	finished := r.now()
	exec.FinishedAt = &finished
}

func (r *ExecutionRuntime) blockExecution(exec *Execution, stepIdx int, category, message string, succeededIndexes []int, def Definition, resolvedInputs map[string]any) {
	if exec == nil {
		return
	}
	step := &exec.Steps[stepIdx]
	step.Status = StepStatusBlocked
	step.Error = strings.TrimSpace(message)
	stepFinished := r.now()
	step.FinishedAt = &stepFinished

	exec.Status = ExecutionStatusBlocked
	exec.Failure = &ExecutionFailure{
		StepID:   def.Steps[stepIdx].ID,
		Category: category,
		Message:  strings.TrimSpace(message),
	}
	r.markRemainingSkipped(exec, stepIdx)
	r.runRollbackChain(exec, succeededIndexes, def, resolvedInputs)
	finished := r.now()
	exec.FinishedAt = &finished
}

func (r *ExecutionRuntime) markRemainingSkipped(exec *Execution, failedStepIdx int) {
	if exec == nil {
		return
	}
	for idx := failedStepIdx + 1; idx < len(exec.Steps); idx++ {
		if exec.Steps[idx].Status != StepStatusPending {
			continue
		}
		now := r.now()
		exec.Steps[idx].Status = StepStatusSkipped
		exec.Steps[idx].StartedAt = &now
		exec.Steps[idx].FinishedAt = &now
	}
}

func (r *ExecutionRuntime) runRollbackChain(exec *Execution, succeededIndexes []int, def Definition, resolvedInputs map[string]any) {
	if exec == nil {
		return
	}
	if len(succeededIndexes) == 0 {
		exec.RollbackStatus = RollbackStatusNotRequired
		return
	}

	exec.RollbackStatus = RollbackStatusCompleted
	rollbackContext := context.Background()

	for idx := len(succeededIndexes) - 1; idx >= 0; idx-- {
		stepIdx := succeededIndexes[idx]
		stepDef := def.Steps[stepIdx]
		if stepDef.Rollback == nil {
			continue
		}

		rollbackStarted := r.now()
		rollbackResult := RollbackExecutionStep{
			StepID:    stepDef.ID,
			Action:    stepDef.Rollback.Action,
			Status:    StepStatusRunning,
			StartedAt: rollbackStarted,
		}

		rollbackParams := resolveStepParameters(stepDef.Rollback.Parameters, resolvedInputs)
		rollbackCtx, cancel := context.WithTimeout(rollbackContext, stepTimeout(stepDef.Rollback.TimeoutSeconds, r.defaultStepTimeout))
		result, runErr := r.actionRunner.Run(rollbackCtx, ActionRequest{
			Definition:  def.Metadata,
			ExecutionID: exec.ID,
			StepID:      stepDef.ID,
			Action:      stepDef.Rollback.Action,
			Parameters:  rollbackParams,
			Inputs:      resolvedInputs,
			Attempt:     1,
			Rollback:    true,
		})
		cancel()

		rollbackFinished := r.now()
		rollbackResult.FinishedAt = &rollbackFinished

		if runErr != nil {
			rollbackResult.Status = StepStatusFailed
			rollbackResult.Error = runErr.Error()
			exec.RollbackStatus = RollbackStatusPartial
		} else {
			rollbackResult.Status = StepStatusSucceeded
			if result != nil {
				rollbackResult.Output = cloneMap(result.Output)
			}
		}

		rollbackCopy := rollbackResult
		exec.Rollback = append(exec.Rollback, rollbackCopy)
		exec.Steps[stepIdx].Rollback = &rollbackCopy
	}

	if len(exec.Rollback) == 0 {
		exec.RollbackStatus = RollbackStatusNotRequired
	}
}

func (r *ExecutionRuntime) persistAndClone(exec *Execution) *Execution {
	if exec == nil {
		return nil
	}
	clone := cloneExecution(exec)
	r.mu.Lock()
	r.executions[exec.ID] = clone
	r.mu.Unlock()
	return cloneExecution(clone)
}

func (r *ExecutionRuntime) nextExecutionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sequence++
	return fmt.Sprintf("apexec-%d-%d", r.now().UnixNano(), r.sequence)
}

func unmetApprovalReason(requirement *ApprovalRequirement, decision ApprovalDecision, scope string) string {
	if requirement == nil || !requirement.Required {
		return ""
	}
	minimumApprovers := requirement.MinimumApprovers
	if minimumApprovers <= 0 {
		minimumApprovers = 1
	}
	if !decision.Approved {
		return fmt.Sprintf("%s approval required (%d approver minimum)", scope, minimumApprovers)
	}
	if decision.ApproverCount > 0 && decision.ApproverCount < minimumApprovers {
		return fmt.Sprintf("%s approval requires %d approvers; got %d", scope, minimumApprovers, decision.ApproverCount)
	}
	if decision.ApproverCount == 0 && minimumApprovers > 1 {
		return fmt.Sprintf("%s approval requires %d approvers", scope, minimumApprovers)
	}
	return ""
}

func normalizeRetryCount(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func timeoutSeconds(stepTimeoutSeconds int, fallback time.Duration) int {
	return int(stepTimeout(stepTimeoutSeconds, fallback).Seconds())
}

func stepTimeout(stepTimeoutSeconds int, fallback time.Duration) time.Duration {
	if stepTimeoutSeconds > 0 {
		return time.Duration(stepTimeoutSeconds) * time.Second
	}
	if fallback <= 0 {
		return 30 * time.Second
	}
	return fallback
}

func inferStepMutating(step Step, resolvedParams map[string]any, idx int) bool {
	if step.Mutating != nil {
		return *step.Mutating
	}

	action := strings.ToLower(strings.TrimSpace(step.Action))
	switch action {
	case "", "run", "run_command", "exec", "execute", "apply", "patch", "delete", "create", "update", "upload_artifact", "rollback":
		if action == "run_command" {
			command := commandPayloadForStep(step, resolvedParams, idx).Command
			return commandLooksMutating(command)
		}
		return true
	case "noop", "read", "read_file", "list", "list_files", "get", "describe", "status", "check", "inventory":
		return false
	default:
		if strings.HasPrefix(action, "read_") || strings.HasPrefix(action, "list_") || strings.HasPrefix(action, "get_") || strings.HasPrefix(action, "describe_") || strings.HasPrefix(action, "check_") {
			return false
		}
		return true
	}
}

func commandLooksMutating(command string) bool {
	text := strings.ToLower(strings.TrimSpace(command))
	if text == "" {
		return true
	}

	readOnlyPrefixes := []string{
		"cat ", "ls", "find ", "grep ", "head ", "tail ", "stat ", "df", "du ", "ps", "top", "id", "whoami", "uname", "echo ", "printf ", "journalctl",
		"kubectl get", "kubectl describe", "systemctl status",
	}
	for _, prefix := range readOnlyPrefixes {
		if strings.HasPrefix(text, prefix) {
			return false
		}
	}

	payload := commandPayloadForStep(Step{ID: "classify", Action: text, Parameters: map[string]any{"command": text}}, map[string]any{"command": text}, 0)
	risk := approval.ClassifyRisk(&payload)
	return risk != "low"
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	type timeoutErr interface {
		Timeout() bool
	}
	var te timeoutErr
	return errors.As(err, &te) && te.Timeout()
}

func cloneExecution(in *Execution) *Execution {
	if in == nil {
		return nil
	}
	out := *in
	out.ResolvedInputs = cloneMap(in.ResolvedInputs)
	out.Steps = make([]ExecutionStep, len(in.Steps))
	for idx := range in.Steps {
		sourceStep := in.Steps[idx]
		outStep := sourceStep
		outStep.ResolvedParameters = cloneMap(sourceStep.ResolvedParameters)
		outStep.Output = cloneMap(sourceStep.Output)
		if len(sourceStep.AttemptHistory) > 0 {
			outStep.AttemptHistory = make([]ExecutionStepAttempt, len(sourceStep.AttemptHistory))
			copy(outStep.AttemptHistory, sourceStep.AttemptHistory)
		}
		if sourceStep.Rollback != nil {
			rollbackCopy := *sourceStep.Rollback
			rollbackCopy.Output = cloneMap(rollbackCopy.Output)
			outStep.Rollback = &rollbackCopy
		}
		out.Steps[idx] = outStep
	}
	if in.Failure != nil {
		failureCopy := *in.Failure
		out.Failure = &failureCopy
	}
	if len(in.Rollback) > 0 {
		out.Rollback = make([]RollbackExecutionStep, len(in.Rollback))
		for idx := range in.Rollback {
			rollbackCopy := in.Rollback[idx]
			rollbackCopy.Output = cloneMap(rollbackCopy.Output)
			out.Rollback[idx] = rollbackCopy
		}
	}
	return &out
}
