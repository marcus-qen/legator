/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RunTrigger describes what initiated an agent run.
// +kubebuilder:validation:Enum=scheduled;webhook;manual
type RunTrigger string

const (
	RunTriggerScheduled RunTrigger = "scheduled"
	RunTriggerWebhook   RunTrigger = "webhook"
	RunTriggerManual    RunTrigger = "manual"
)

// RunPhase represents the lifecycle phase of an agent run.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Escalated;Blocked
type RunPhase string

const (
	RunPhasePending   RunPhase = "Pending"
	RunPhaseRunning   RunPhase = "Running"
	RunPhaseSucceeded RunPhase = "Succeeded"
	RunPhaseFailed    RunPhase = "Failed"
	RunPhaseEscalated RunPhase = "Escalated"
	RunPhaseBlocked   RunPhase = "Blocked"
)

// ActionTier classifies the risk level of a tool action.
// +kubebuilder:validation:Enum=read;service-mutation;destructive-mutation;data-mutation
type ActionTier string

const (
	ActionTierRead                ActionTier = "read"
	ActionTierServiceMutation     ActionTier = "service-mutation"
	ActionTierDestructiveMutation ActionTier = "destructive-mutation"
	ActionTierDataMutation        ActionTier = "data-mutation"
)

// ActionStatus describes what happened to a tool call.
// +kubebuilder:validation:Enum=executed;blocked;failed;skipped
type ActionStatus string

const (
	ActionStatusExecuted        ActionStatus = "executed"
	ActionStatusBlocked         ActionStatus = "blocked"
	ActionStatusFailed          ActionStatus = "failed"
	ActionStatusSkipped         ActionStatus = "skipped"
	ActionStatusApproved        ActionStatus = "approved"
	ActionStatusDenied          ActionStatus = "denied"
	ActionStatusPendingApproval ActionStatus = "pending-approval"
)

// FindingSeverity classifies an agent finding.
// +kubebuilder:validation:Enum=info;warning;critical
type FindingSeverity string

const (
	FindingSeverityInfo     FindingSeverity = "info"
	FindingSeverityWarning  FindingSeverity = "warning"
	FindingSeverityCritical FindingSeverity = "critical"
)

// --- Sub-types ---

// PreFlightResult captures the result of pre-flight checks for an action.
type PreFlightResult struct {
	// autonomyCheck indicates whether the autonomy level permits this action.
	// +optional
	AutonomyCheck string `json:"autonomyCheck,omitempty"`

	// dataImpactCheck indicates whether the action impacts data resources.
	// +optional
	DataImpactCheck string `json:"dataImpactCheck,omitempty"`

	// allowListCheck indicates whether the action matches the allow list.
	// +optional
	AllowListCheck string `json:"allowListCheck,omitempty"`

	// approvalCheck indicates whether approval was required in pre-flight.
	// +optional
	ApprovalCheck string `json:"approvalCheck,omitempty"`

	// approvalDecision records the approval outcome when approval was required.
	// +optional
	ApprovalDecision string `json:"approvalDecision,omitempty"`

	// safetyGateOutcome is the final pre-execution guardrail outcome.
	// Typical values: ALLOW, NEEDS_APPROVAL, APPROVED, DENIED, BLOCKED, EXPIRED.
	// +optional
	SafetyGateOutcome string `json:"safetyGateOutcome,omitempty"`

	// dataProtection indicates whether hardcoded data protection rules blocked this action.
	// +optional
	DataProtection string `json:"dataProtection,omitempty"`

	// reason provides a human-readable explanation when a check fails.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// ActionRecord captures a single tool call attempt.
type ActionRecord struct {
	// seq is the sequence number within this run.
	// +required
	Seq int32 `json:"seq"`

	// timestamp is when this action was attempted.
	// +required
	Timestamp metav1.Time `json:"timestamp"`

	// tool is the tool identifier (e.g. "kubectl.get", "http.post", "mcp.k8sgpt.analyze").
	// +required
	Tool string `json:"tool"`

	// target is what the tool acted on (e.g. "pods -n backstage").
	// +required
	Target string `json:"target"`

	// tier is the risk classification of this action.
	// +required
	Tier ActionTier `json:"tier"`

	// preFlightCheck captures the pre-flight check results.
	// +optional
	PreFlightCheck *PreFlightResult `json:"preFlightCheck,omitempty"`

	// result is the tool output or error message.
	// +optional
	Result string `json:"result,omitempty"`

	// status is what happened to this action.
	// +required
	Status ActionStatus `json:"status"`

	// escalation captures escalation details when an action is blocked.
	// +optional
	Escalation *ActionEscalation `json:"escalation,omitempty"`
}

// ActionEscalation records an escalation triggered by a blocked action.
type ActionEscalation struct {
	// channel is where the escalation was sent.
	// +required
	Channel string `json:"channel"`

	// message is the escalation content.
	// +required
	Message string `json:"message"`

	// timestamp is when the escalation was sent.
	// +required
	Timestamp metav1.Time `json:"timestamp"`
}

// GuardrailSummary captures aggregate guardrail activity for a run.
type GuardrailSummary struct {
	// checksPerformed is the total number of pre-flight checks.
	// +optional
	ChecksPerformed int32 `json:"checksPerformed,omitempty"`

	// actionsBlocked is how many actions were blocked by guardrails.
	// +optional
	ActionsBlocked int32 `json:"actionsBlocked,omitempty"`

	// escalationsTriggered is how many escalations were sent.
	// +optional
	EscalationsTriggered int32 `json:"escalationsTriggered,omitempty"`

	// autonomyCeiling is the agent's configured autonomy level.
	// +optional
	AutonomyCeiling AutonomyLevel `json:"autonomyCeiling,omitempty"`

	// budgetUsed reports resource consumption.
	// +optional
	BudgetUsed *BudgetUsage `json:"budgetUsed,omitempty"`
}

// BudgetUsage reports token, iteration, and time consumption.
type BudgetUsage struct {
	// tokensUsed is the actual tokens consumed.
	// +optional
	TokensUsed int64 `json:"tokensUsed,omitempty"`

	// tokenBudget is the configured max.
	// +optional
	TokenBudget int64 `json:"tokenBudget,omitempty"`

	// iterationsUsed is the actual iterations.
	// +optional
	IterationsUsed int32 `json:"iterationsUsed,omitempty"`

	// maxIterations is the configured max.
	// +optional
	MaxIterations int32 `json:"maxIterations,omitempty"`

	// wallClockMs is the actual run duration in milliseconds.
	// +optional
	WallClockMs int64 `json:"wallClockMs,omitempty"`

	// timeoutMs is the configured max in milliseconds.
	// +optional
	TimeoutMs int64 `json:"timeoutMs,omitempty"`
}

// RunFinding records something noteworthy the agent discovered.
type RunFinding struct {
	// severity classifies the finding.
	// +required
	Severity FindingSeverity `json:"severity"`

	// resource is the Kubernetes resource the finding relates to.
	// +optional
	Resource string `json:"resource,omitempty"`

	// message is a human-readable description.
	// +required
	Message string `json:"message"`
}

// UsageSummary records resource consumption for a run.
type UsageSummary struct {
	// tokensIn is the input token count.
	// +optional
	TokensIn int64 `json:"tokensIn,omitempty"`

	// tokensOut is the output token count.
	// +optional
	TokensOut int64 `json:"tokensOut,omitempty"`

	// totalTokens is tokensIn + tokensOut.
	// +optional
	TotalTokens int64 `json:"totalTokens,omitempty"`

	// iterations is the number of tool-call loops.
	// +optional
	Iterations int32 `json:"iterations,omitempty"`

	// wallClockMs is the total run duration in milliseconds.
	// +optional
	WallClockMs int64 `json:"wallClockMs,omitempty"`

	// estimatedCost is the estimated USD cost (based on ModelTierConfig pricing).
	// +optional
	EstimatedCost string `json:"estimatedCost,omitempty"`
}

// --- LegatorRun spec and status ---

// LegatorRunSpec defines the immutable configuration for a run.
type LegatorRunSpec struct {
	// agentRef is the name of the LegatorAgent that owns this run.
	// +required
	AgentRef string `json:"agentRef"`

	// environmentRef is the name of the LegatorEnvironment used.
	// +required
	EnvironmentRef string `json:"environmentRef"`

	// trigger describes what initiated this run.
	// +required
	Trigger RunTrigger `json:"trigger"`

	// modelUsed is the actual provider/model resolved from the tier.
	// +optional
	ModelUsed string `json:"modelUsed,omitempty"`
}

// LegatorRunStatus defines the observed state of an LegatorRun.
// Once phase reaches a terminal state (Succeeded/Failed/Escalated/Blocked),
// no field is ever modified again.
type LegatorRunStatus struct {
	// phase is the current lifecycle phase.
	// +optional
	Phase RunPhase `json:"phase,omitempty"`

	// startTime is when the run began.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// completionTime is when the run finished.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// usage summarises resource consumption.
	// +optional
	Usage *UsageSummary `json:"usage,omitempty"`

	// actions is the ordered list of every tool call attempted.
	// +optional
	Actions []ActionRecord `json:"actions,omitempty"`

	// guardrails summarises guardrail activity.
	// +optional
	Guardrails *GuardrailSummary `json:"guardrails,omitempty"`

	// findings lists noteworthy discoveries.
	// +optional
	Findings []RunFinding `json:"findings,omitempty"`

	// report is the agent's human-readable summary.
	// +optional
	Report string `json:"report,omitempty"`

	// conditions represent the current state.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Agent",type="string",JSONPath=".spec.agentRef"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Trigger",type="string",JSONPath=".spec.trigger"
// +kubebuilder:printcolumn:name="Duration",type="integer",JSONPath=".status.usage.wallClockMs",priority=1
// +kubebuilder:printcolumn:name="Tokens",type="integer",JSONPath=".status.usage.totalTokens",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// LegatorRun is the Schema for the agentruns API.
// It is an immutable audit record of a single agent execution —
// every action, pre-flight check, block, escalation, and finding.
type LegatorRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec   LegatorRunSpec   `json:"spec"`
	Status LegatorRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LegatorRunList contains a list of LegatorRun.
type LegatorRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LegatorRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LegatorRun{}, &LegatorRunList{})
}
