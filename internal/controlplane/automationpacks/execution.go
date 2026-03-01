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

	TimelineEventExecutionStarted   = "execution.started"
	TimelineEventExecutionFinished  = "execution.finished"
	TimelineEventStepStarted        = "step.started"
	TimelineEventStepPolicy         = "step.policy_evaluated"
	TimelineEventStepApprovalCheck  = "step.approval_checkpoint"
	TimelineEventStepApprovalResult = "step.approval_decision"
	TimelineEventStepAttemptStarted = "step.attempt.started"
	TimelineEventStepAttemptResult  = "step.attempt.result"
	TimelineEventStepFinished       = "step.finished"
	TimelineEventStepBlocked        = "step.blocked"
	TimelineEventStepSkipped        = "step.skipped"
	TimelineEventRollbackStarted    = "rollback.started"
	TimelineEventRollbackFinished   = "rollback.finished"

	ArtifactTypeStdoutSnippet   = "stdout_snippet"
	ArtifactTypeStderrSnippet   = "stderr_snippet"
	ArtifactTypeErrorContext    = "error_context"
	ArtifactTypePolicyRationale = "policy_rationale"
	ArtifactTypeApproval        = "approval_checkpoint"
	ArtifactTypeActionMessage   = "action_message"
	ArtifactTypeActionPayload   = "action_payload"
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

// ApprovalCheckpoint captures one runtime approval checkpoint and decision.
type ApprovalCheckpoint struct {
	Scope            string   `json:"scope"`
	Required         bool     `json:"required"`
	MinimumApprovers int      `json:"minimum_approvers,omitempty"`
	Approved         bool     `json:"approved"`
	ApproverCount    int      `json:"approver_count,omitempty"`
	ApprovedBy       []string `json:"approved_by,omitempty"`
	Note             string   `json:"note,omitempty"`
	Reason           string   `json:"reason,omitempty"`
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
	ID             string                   `json:"id"`
	Metadata       Metadata                 `json:"metadata"`
	Status         string                   `json:"status"`
	StartedAt      time.Time                `json:"started_at"`
	FinishedAt     *time.Time               `json:"finished_at,omitempty"`
	ResolvedInputs map[string]any           `json:"resolved_inputs,omitempty"`
	Steps          []ExecutionStep          `json:"steps"`
	Failure        *ExecutionFailure        `json:"failure,omitempty"`
	RollbackStatus string                   `json:"rollback_status,omitempty"`
	Rollback       []RollbackExecutionStep  `json:"rollback,omitempty"`
	Timeline       []ExecutionTimelineEvent `json:"timeline,omitempty"`
	Artifacts      []ExecutionArtifact      `json:"artifacts,omitempty"`
}

// ExecutionTimelineEvent captures an ordered execution lifecycle event.
type ExecutionTimelineEvent struct {
	ID        string         `json:"id"`
	Sequence  int            `json:"sequence"`
	Timestamp time.Time      `json:"timestamp"`
	Type      string         `json:"type"`
	StepID    string         `json:"step_id,omitempty"`
	Attempt   int            `json:"attempt,omitempty"`
	Status    string         `json:"status,omitempty"`
	Message   string         `json:"message,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// ExecutionArtifact captures replay/debug evidence produced during execution.
type ExecutionArtifact struct {
	ID        string         `json:"id"`
	EventID   string         `json:"event_id,omitempty"`
	StepID    string         `json:"step_id,omitempty"`
	Attempt   int            `json:"attempt,omitempty"`
	Type      string         `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}

// ExecutionReplay describes deterministic event ordering for replay clients.
type ExecutionReplay struct {
	ExecutionID     string     `json:"execution_id"`
	Deterministic   bool       `json:"deterministic_order"`
	EventCount      int        `json:"event_count"`
	ArtifactCount   int        `json:"artifact_count"`
	OrderedEventIDs []string   `json:"ordered_event_ids"`
	FirstTimestamp  *time.Time `json:"first_timestamp,omitempty"`
	LastTimestamp   *time.Time `json:"last_timestamp,omitempty"`
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
	Output        map[string]any `json:"output,omitempty"`
	Message       string         `json:"message,omitempty"`
	StdoutSnippet string         `json:"stdout_snippet,omitempty"`
	StderrSnippet string         `json:"stderr_snippet,omitempty"`
	Artifacts     map[string]any `json:"artifacts,omitempty"`
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
	r.recordTimeline(exec, timelineEventInput{
		Type:      TimelineEventExecutionStarted,
		Status:    ExecutionStatusRunning,
		Message:   "execution started",
		Timestamp: now,
		Data: map[string]any{
			"definition_id":      def.Metadata.ID,
			"definition_version": def.Metadata.Version,
		},
	})

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
		r.recordTimeline(exec, timelineEventInput{
			Type:      TimelineEventStepStarted,
			StepID:    stepDef.ID,
			Status:    StepStatusRunning,
			Message:   "step started",
			Timestamp: stepStarted,
			Data: map[string]any{
				"order":    step.Order,
				"action":   step.Action,
				"mutating": step.Mutating,
			},
		})

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

			policyEventID := r.recordTimeline(exec, timelineEventInput{
				Type:      TimelineEventStepPolicy,
				StepID:    stepDef.ID,
				Status:    policy.Outcome,
				Message:   strings.TrimSpace(policy.Summary),
				Timestamp: r.now(),
				Data: map[string]any{
					"outcome":    policy.Outcome,
					"risk_level": policy.RiskLevel,
					"summary":    strings.TrimSpace(policy.Summary),
					"rationale":  cloneValue(policy.Rationale),
				},
			})
			r.recordArtifact(exec, artifactInput{
				EventID:   policyEventID,
				StepID:    stepDef.ID,
				Type:      ArtifactTypePolicyRationale,
				Timestamp: r.now(),
				Data: map[string]any{
					"outcome":    policy.Outcome,
					"risk_level": policy.RiskLevel,
					"summary":    strings.TrimSpace(policy.Summary),
					"rationale":  cloneValue(policy.Rationale),
				},
			})

			switch policy.Outcome {
			case PolicyOutcomeDeny:
				r.blockExecution(exec, idx, "policy", fmt.Sprintf("step %s denied by policy: %s", stepDef.ID, strings.TrimSpace(policy.Summary)), succeededIndexes, *def, resolvedInputs)
				return r.persistAndClone(exec), nil
			case PolicyOutcomeQueue:
				r.blockExecution(exec, idx, "policy", fmt.Sprintf("step %s requires approval by policy gate", stepDef.ID), succeededIndexes, *def, resolvedInputs)
				return r.persistAndClone(exec), nil
			}

			if checkpoint := evaluateApprovalCheckpoint(def.Approval, req.ApprovalContext.Workflow, "workflow"); checkpoint != nil {
				eventID := r.recordApprovalCheckpoint(exec, stepDef.ID, checkpoint)
				if !checkpoint.Approved {
					r.recordArtifact(exec, artifactInput{
						EventID:   eventID,
						StepID:    stepDef.ID,
						Type:      ArtifactTypeErrorContext,
						Timestamp: r.now(),
						Data: map[string]any{
							"category": "approval",
							"scope":    checkpoint.Scope,
							"reason":   checkpoint.Reason,
						},
					})
					r.blockExecution(exec, idx, "approval", checkpoint.Reason, succeededIndexes, *def, resolvedInputs)
					return r.persistAndClone(exec), nil
				}
			}
			if checkpoint := evaluateApprovalCheckpoint(stepDef.Approval, req.ApprovalContext.stepDecision(stepDef.ID), fmt.Sprintf("step %s", stepDef.ID)); checkpoint != nil {
				eventID := r.recordApprovalCheckpoint(exec, stepDef.ID, checkpoint)
				if !checkpoint.Approved {
					r.recordArtifact(exec, artifactInput{
						EventID:   eventID,
						StepID:    stepDef.ID,
						Type:      ArtifactTypeErrorContext,
						Timestamp: r.now(),
						Data: map[string]any{
							"category": "approval",
							"scope":    checkpoint.Scope,
							"reason":   checkpoint.Reason,
						},
					})
					r.blockExecution(exec, idx, "approval", checkpoint.Reason, succeededIndexes, *def, resolvedInputs)
					return r.persistAndClone(exec), nil
				}
			}
		}

		maxAttempts := step.MaxRetries + 1
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			attemptStarted := r.now()
			attemptState := ExecutionStepAttempt{Attempt: attempt, Status: StepStatusRunning, StartedAt: attemptStarted}
			r.recordTimeline(exec, timelineEventInput{
				Type:      TimelineEventStepAttemptStarted,
				StepID:    stepDef.ID,
				Attempt:   attempt,
				Status:    StepStatusRunning,
				Message:   "step attempt started",
				Timestamp: attemptStarted,
			})

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

				attemptEventID := r.recordTimeline(exec, timelineEventInput{
					Type:      TimelineEventStepAttemptResult,
					StepID:    stepDef.ID,
					Attempt:   attempt,
					Status:    StepStatusSucceeded,
					Message:   "step attempt succeeded",
					Timestamp: attemptFinished,
				})
				r.captureActionArtifacts(exec, attemptEventID, stepDef.ID, attempt, result, attemptFinished)

				stepFinished := r.now()
				step.FinishedAt = &stepFinished
				r.recordTimeline(exec, timelineEventInput{
					Type:      TimelineEventStepFinished,
					StepID:    stepDef.ID,
					Status:    StepStatusSucceeded,
					Message:   "step completed",
					Timestamp: stepFinished,
					Data: map[string]any{
						"attempts": step.Attempts,
					},
				})
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

			attemptEventID := r.recordTimeline(exec, timelineEventInput{
				Type:      TimelineEventStepAttemptResult,
				StepID:    stepDef.ID,
				Attempt:   attempt,
				Status:    attemptState.Status,
				Message:   runErr.Error(),
				Timestamp: attemptFinished,
			})
			r.recordArtifact(exec, artifactInput{
				EventID:   attemptEventID,
				StepID:    stepDef.ID,
				Attempt:   attempt,
				Type:      ArtifactTypeErrorContext,
				Timestamp: attemptFinished,
				Data: map[string]any{
					"phase":   "step",
					"error":   runErr.Error(),
					"timeout": isTimeoutError(runErr),
					"action":  stepDef.Action,
				},
			})

			if attempt == maxAttempts {
				if isTimeoutError(runErr) {
					step.Status = StepStatusTimedOut
				} else {
					step.Status = StepStatusFailed
				}
				stepFinished := r.now()
				step.FinishedAt = &stepFinished
				r.recordTimeline(exec, timelineEventInput{
					Type:      TimelineEventStepFinished,
					StepID:    stepDef.ID,
					Status:    step.Status,
					Message:   strings.TrimSpace(runErr.Error()),
					Timestamp: stepFinished,
					Data:      map[string]any{"attempts": step.Attempts},
				})
				r.failExecution(exec, idx, fmt.Sprintf("step %s failed: %s", stepDef.ID, runErr.Error()), succeededIndexes, *def, resolvedInputs)
				return r.persistAndClone(exec), nil
			}
		}
	}

	exec.Status = ExecutionStatusSucceeded
	finished := r.now()
	exec.FinishedAt = &finished
	exec.RollbackStatus = RollbackStatusNotRequired
	r.recordTimeline(exec, timelineEventInput{
		Type:      TimelineEventExecutionFinished,
		Status:    ExecutionStatusSucceeded,
		Message:   "execution completed",
		Timestamp: finished,
		Data: map[string]any{
			"rollback_status": exec.RollbackStatus,
		},
	})
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

func (r *ExecutionRuntime) GetTimeline(executionID string) ([]ExecutionTimelineEvent, error) {
	exec, err := r.Get(executionID)
	if err != nil {
		return nil, err
	}
	if len(exec.Timeline) == 0 {
		return nil, nil
	}
	out := make([]ExecutionTimelineEvent, len(exec.Timeline))
	for idx := range exec.Timeline {
		evt := exec.Timeline[idx]
		evt.Data = cloneMap(evt.Data)
		out[idx] = evt
	}
	return out, nil
}

func (r *ExecutionRuntime) GetArtifacts(executionID string) ([]ExecutionArtifact, error) {
	exec, err := r.Get(executionID)
	if err != nil {
		return nil, err
	}
	if len(exec.Artifacts) == 0 {
		return nil, nil
	}
	out := make([]ExecutionArtifact, len(exec.Artifacts))
	for idx := range exec.Artifacts {
		artifact := exec.Artifacts[idx]
		artifact.Data = cloneMap(artifact.Data)
		out[idx] = artifact
	}
	return out, nil
}

func (r *ExecutionRuntime) GetReplay(executionID string) (*ExecutionReplay, error) {
	exec, err := r.Get(executionID)
	if err != nil {
		return nil, err
	}
	return buildExecutionReplay(exec), nil
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
	r.recordArtifact(exec, artifactInput{
		StepID:    def.Steps[stepIdx].ID,
		Type:      ArtifactTypeErrorContext,
		Timestamp: r.now(),
		Data: map[string]any{
			"category": "execution",
			"message":  strings.TrimSpace(message),
		},
	})
	r.markRemainingSkipped(exec, stepIdx)
	r.runRollbackChain(exec, succeededIndexes, def, resolvedInputs)
	finished := r.now()
	exec.FinishedAt = &finished
	r.recordTimeline(exec, timelineEventInput{
		Type:      TimelineEventExecutionFinished,
		Status:    ExecutionStatusFailed,
		Message:   strings.TrimSpace(message),
		Timestamp: finished,
		Data: map[string]any{
			"rollback_status": exec.RollbackStatus,
		},
	})
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
	blockedEventID := r.recordTimeline(exec, timelineEventInput{
		Type:      TimelineEventStepBlocked,
		StepID:    step.ID,
		Status:    StepStatusBlocked,
		Message:   strings.TrimSpace(message),
		Timestamp: stepFinished,
		Data: map[string]any{
			"category": category,
		},
	})
	r.recordArtifact(exec, artifactInput{
		EventID:   blockedEventID,
		StepID:    step.ID,
		Type:      ArtifactTypeErrorContext,
		Timestamp: stepFinished,
		Data: map[string]any{
			"category": category,
			"message":  strings.TrimSpace(message),
		},
	})

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
	r.recordTimeline(exec, timelineEventInput{
		Type:      TimelineEventExecutionFinished,
		Status:    ExecutionStatusBlocked,
		Message:   strings.TrimSpace(message),
		Timestamp: finished,
		Data: map[string]any{
			"rollback_status": exec.RollbackStatus,
			"category":        category,
		},
	})
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
		r.recordTimeline(exec, timelineEventInput{
			Type:      TimelineEventStepSkipped,
			StepID:    exec.Steps[idx].ID,
			Status:    StepStatusSkipped,
			Message:   "step skipped",
			Timestamp: now,
		})
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
		startedEventID := r.recordTimeline(exec, timelineEventInput{
			Type:      TimelineEventRollbackStarted,
			StepID:    stepDef.ID,
			Attempt:   1,
			Status:    StepStatusRunning,
			Message:   "rollback started",
			Timestamp: rollbackStarted,
			Data: map[string]any{
				"action": stepDef.Rollback.Action,
			},
		})

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
			r.recordArtifact(exec, artifactInput{
				EventID:   startedEventID,
				StepID:    stepDef.ID,
				Attempt:   1,
				Type:      ArtifactTypeErrorContext,
				Timestamp: rollbackFinished,
				Data: map[string]any{
					"phase":   "rollback",
					"error":   runErr.Error(),
					"action":  stepDef.Rollback.Action,
					"timeout": isTimeoutError(runErr),
				},
			})
		} else {
			rollbackResult.Status = StepStatusSucceeded
			if result != nil {
				rollbackResult.Output = cloneMap(result.Output)
			}
			r.captureActionArtifacts(exec, startedEventID, stepDef.ID, 1, result, rollbackFinished)
		}

		r.recordTimeline(exec, timelineEventInput{
			Type:      TimelineEventRollbackFinished,
			StepID:    stepDef.ID,
			Attempt:   1,
			Status:    rollbackResult.Status,
			Message:   strings.TrimSpace(rollbackResult.Error),
			Timestamp: rollbackFinished,
			Data: map[string]any{
				"action": stepDef.Rollback.Action,
			},
		})

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

type timelineEventInput struct {
	Type      string
	StepID    string
	Attempt   int
	Status    string
	Message   string
	Timestamp time.Time
	Data      map[string]any
}

type artifactInput struct {
	EventID   string
	StepID    string
	Attempt   int
	Type      string
	Timestamp time.Time
	Data      map[string]any
}

func evaluateApprovalCheckpoint(requirement *ApprovalRequirement, decision ApprovalDecision, scope string) *ApprovalCheckpoint {
	if requirement == nil || !requirement.Required {
		return nil
	}
	minimumApprovers := requirement.MinimumApprovers
	if minimumApprovers <= 0 {
		minimumApprovers = 1
	}
	checkpoint := &ApprovalCheckpoint{
		Scope:            strings.TrimSpace(scope),
		Required:         true,
		MinimumApprovers: minimumApprovers,
		ApproverCount:    decision.ApproverCount,
		ApprovedBy:       append([]string(nil), decision.ApprovedBy...),
		Note:             strings.TrimSpace(decision.Note),
	}
	switch {
	case !decision.Approved:
		checkpoint.Reason = fmt.Sprintf("%s approval required (%d approver minimum)", scope, minimumApprovers)
	case decision.ApproverCount > 0 && decision.ApproverCount < minimumApprovers:
		checkpoint.Reason = fmt.Sprintf("%s approval requires %d approvers; got %d", scope, minimumApprovers, decision.ApproverCount)
	case decision.ApproverCount == 0 && minimumApprovers > 1:
		checkpoint.Reason = fmt.Sprintf("%s approval requires %d approvers", scope, minimumApprovers)
	}
	checkpoint.Approved = strings.TrimSpace(checkpoint.Reason) == ""
	if checkpoint.Approved {
		checkpoint.Reason = "approval requirement satisfied"
	}
	return checkpoint
}

func (r *ExecutionRuntime) recordApprovalCheckpoint(exec *Execution, stepID string, checkpoint *ApprovalCheckpoint) string {
	if checkpoint == nil {
		return ""
	}
	status := "blocked"
	if checkpoint.Approved {
		status = "approved"
	}
	checkpointData := map[string]any{
		"scope":             checkpoint.Scope,
		"required":          checkpoint.Required,
		"minimum_approvers": checkpoint.MinimumApprovers,
		"approved":          checkpoint.Approved,
		"approver_count":    checkpoint.ApproverCount,
		"approved_by":       append([]string(nil), checkpoint.ApprovedBy...),
		"note":              checkpoint.Note,
		"reason":            checkpoint.Reason,
	}
	eventID := r.recordTimeline(exec, timelineEventInput{
		Type:      TimelineEventStepApprovalCheck,
		StepID:    stepID,
		Status:    status,
		Message:   checkpoint.Reason,
		Timestamp: r.now(),
		Data:      checkpointData,
	})
	r.recordTimeline(exec, timelineEventInput{
		Type:      TimelineEventStepApprovalResult,
		StepID:    stepID,
		Status:    status,
		Message:   checkpoint.Reason,
		Timestamp: r.now(),
		Data:      checkpointData,
	})
	r.recordArtifact(exec, artifactInput{
		EventID:   eventID,
		StepID:    stepID,
		Type:      ArtifactTypeApproval,
		Timestamp: r.now(),
		Data:      checkpointData,
	})
	return eventID
}

func (r *ExecutionRuntime) recordTimeline(exec *Execution, input timelineEventInput) string {
	if exec == nil {
		return ""
	}
	timestamp := input.Timestamp
	if timestamp.IsZero() {
		timestamp = r.now()
	}
	sequence := len(exec.Timeline) + 1
	eventID := fmt.Sprintf("%s-evt-%06d", exec.ID, sequence)
	event := ExecutionTimelineEvent{
		ID:        eventID,
		Sequence:  sequence,
		Timestamp: timestamp,
		Type:      strings.TrimSpace(input.Type),
		StepID:    strings.TrimSpace(input.StepID),
		Attempt:   input.Attempt,
		Status:    strings.TrimSpace(input.Status),
		Message:   strings.TrimSpace(input.Message),
		Data:      cloneMap(input.Data),
	}
	exec.Timeline = append(exec.Timeline, event)
	return eventID
}

func (r *ExecutionRuntime) recordArtifact(exec *Execution, input artifactInput) string {
	if exec == nil {
		return ""
	}
	timestamp := input.Timestamp
	if timestamp.IsZero() {
		timestamp = r.now()
	}
	sequence := len(exec.Artifacts) + 1
	artifactID := fmt.Sprintf("%s-art-%06d", exec.ID, sequence)
	artifact := ExecutionArtifact{
		ID:        artifactID,
		EventID:   strings.TrimSpace(input.EventID),
		StepID:    strings.TrimSpace(input.StepID),
		Attempt:   input.Attempt,
		Type:      strings.TrimSpace(input.Type),
		Timestamp: timestamp,
		Data:      cloneMap(input.Data),
	}
	exec.Artifacts = append(exec.Artifacts, artifact)
	return artifactID
}

func (r *ExecutionRuntime) captureActionArtifacts(exec *Execution, eventID, stepID string, attempt int, result *ActionResult, timestamp time.Time) {
	if exec == nil || result == nil {
		return
	}
	stdout := trimSnippet(result.StdoutSnippet)
	if stdout != "" {
		r.recordArtifact(exec, artifactInput{
			EventID:   eventID,
			StepID:    stepID,
			Attempt:   attempt,
			Type:      ArtifactTypeStdoutSnippet,
			Timestamp: timestamp,
			Data:      map[string]any{"snippet": stdout},
		})
	}
	stderr := trimSnippet(result.StderrSnippet)
	if stderr != "" {
		r.recordArtifact(exec, artifactInput{
			EventID:   eventID,
			StepID:    stepID,
			Attempt:   attempt,
			Type:      ArtifactTypeStderrSnippet,
			Timestamp: timestamp,
			Data:      map[string]any{"snippet": stderr},
		})
	}
	if text := strings.TrimSpace(result.Message); text != "" {
		r.recordArtifact(exec, artifactInput{
			EventID:   eventID,
			StepID:    stepID,
			Attempt:   attempt,
			Type:      ArtifactTypeActionMessage,
			Timestamp: timestamp,
			Data:      map[string]any{"message": trimSnippet(text)},
		})
	}
	if len(result.Artifacts) > 0 {
		r.recordArtifact(exec, artifactInput{
			EventID:   eventID,
			StepID:    stepID,
			Attempt:   attempt,
			Type:      ArtifactTypeActionPayload,
			Timestamp: timestamp,
			Data:      cloneMap(result.Artifacts),
		})
	}
}

func trimSnippet(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	const maxSnippetLen = 1024
	if len(trimmed) <= maxSnippetLen {
		return trimmed
	}
	return trimmed[:maxSnippetLen] + "…"
}

func buildExecutionReplay(exec *Execution) *ExecutionReplay {
	if exec == nil {
		return nil
	}
	replay := &ExecutionReplay{
		ExecutionID:     exec.ID,
		Deterministic:   true,
		EventCount:      len(exec.Timeline),
		ArtifactCount:   len(exec.Artifacts),
		OrderedEventIDs: make([]string, 0, len(exec.Timeline)),
	}
	if len(exec.Timeline) > 0 {
		first := exec.Timeline[0].Timestamp
		last := exec.Timeline[len(exec.Timeline)-1].Timestamp
		replay.FirstTimestamp = &first
		replay.LastTimestamp = &last
	}
	for _, event := range exec.Timeline {
		replay.OrderedEventIDs = append(replay.OrderedEventIDs, event.ID)
	}
	return replay
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
	if len(in.Timeline) > 0 {
		out.Timeline = make([]ExecutionTimelineEvent, len(in.Timeline))
		for idx := range in.Timeline {
			eventCopy := in.Timeline[idx]
			eventCopy.Data = cloneMap(eventCopy.Data)
			out.Timeline[idx] = eventCopy
		}
	}
	if len(in.Artifacts) > 0 {
		out.Artifacts = make([]ExecutionArtifact, len(in.Artifacts))
		for idx := range in.Artifacts {
			artifactCopy := in.Artifacts[idx]
			artifactCopy.Data = cloneMap(artifactCopy.Data)
			out.Artifacts[idx] = artifactCopy
		}
	}
	return &out
}
