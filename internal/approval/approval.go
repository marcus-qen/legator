/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package approval implements the approval workflow for actions that exceed
// an agent's autonomy level. When the engine determines an action needs
// human approval, this package:
//
//  1. Creates an ApprovalRequest CRD
//  2. Polls for a decision (approved/denied/expired)
//  3. Returns the result to the runner
//
// The ApprovalRequest can be approved via:
//   - Dashboard UI (POST /approvals/<name>/approve)
//   - CLI: kubectl patch / legator approve
//   - Future: Slack/Telegram bot
package approval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/marcus-qen/legator/api/v1alpha1"
)

// Result describes the outcome of an approval request.
type Result struct {
	// Approved is true if the action may proceed.
	Approved bool

	// Phase is the final phase of the ApprovalRequest.
	Phase corev1alpha1.ApprovalRequestPhase

	// DecidedBy is who approved/denied (empty if expired).
	DecidedBy string

	// Reason is the approver's stated reason.
	Reason string
}

// Manager creates and monitors ApprovalRequests.
type Manager struct {
	client      client.Client
	log         logr.Logger
	pollInterval time.Duration
}

const (
	// AnnotationTypedConfirmationRequired indicates that approval requires typed confirmation.
	AnnotationTypedConfirmationRequired = "legator.io/typed-confirmation-required"
	// AnnotationTypedConfirmationToken stores expected typed confirmation value.
	AnnotationTypedConfirmationToken = "legator.io/typed-confirmation-token"
	// AnnotationTypedConfirmationExpiresAt stores RFC3339 expiry timestamp for confirmation token.
	AnnotationTypedConfirmationExpiresAt = "legator.io/typed-confirmation-expires-at"
)

// NewManager creates an approval manager.
func NewManager(c client.Client, log logr.Logger) *Manager {
	return &Manager{
		client:       c,
		log:          log,
		pollInterval: 5 * time.Second,
	}
}

// RequestApproval creates an ApprovalRequest and waits for a decision.
// It blocks until the request is approved, denied, or times out.
// The context should carry the run's deadline — if the run timeout expires,
// the approval is abandoned.
func (m *Manager) RequestApproval(ctx context.Context, req ApprovalParams) (*Result, error) {
	timeout := parseApprovalTimeout(req.Timeout)
	deadline := time.Now().Add(timeout)
	contextText := req.Context

	annotations := map[string]string{}
	if RequiresTypedConfirmation(req.Tier) {
		token, err := generateTypedConfirmationToken()
		if err != nil {
			return nil, fmt.Errorf("generate typed confirmation token: %w", err)
		}
		annotations[AnnotationTypedConfirmationRequired] = "true"
		annotations[AnnotationTypedConfirmationToken] = token
		annotations[AnnotationTypedConfirmationExpiresAt] = deadline.UTC().Format(time.RFC3339)
		contextText = appendContextInstruction(contextText, fmt.Sprintf("Typed confirmation required. Re-enter token exactly: %s", token))
	}

	// Create the ApprovalRequest CRD
	ar := &corev1alpha1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-approval-", req.AgentName),
			Namespace:    req.Namespace,
			Labels: map[string]string{
				"legator.io/agent": req.AgentName,
				"legator.io/run":   req.RunName,
				"legator.io/tool":  sanitizeLabel(req.Tool),
			},
			Annotations: annotations,
		},
		Spec: corev1alpha1.ApprovalRequestSpec{
			AgentName: req.AgentName,
			RunName:   req.RunName,
			Action: corev1alpha1.ProposedAction{
				Tool:        req.Tool,
				Tier:        string(req.Tier),
				Target:      req.Target,
				Description: req.Description,
				Args:        req.Args,
			},
			Context:  contextText,
			Timeout:  req.Timeout,
			Channels: req.Channels,
		},
	}

	ar.Status.Phase = corev1alpha1.ApprovalPhasePending

	if err := m.client.Create(ctx, ar); err != nil {
		return nil, fmt.Errorf("create ApprovalRequest: %w", err)
	}

	m.log.Info("ApprovalRequest created — waiting for decision",
		"name", ar.Name,
		"agent", req.AgentName,
		"tool", req.Tool,
		"target", req.Target,
		"timeout", req.Timeout,
		"typedConfirmationRequired", annotations[AnnotationTypedConfirmationRequired] == "true",
	)

	// Poll for decision
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Run context expired — mark as expired
			m.expireRequest(context.Background(), ar)
			return &Result{
				Phase: corev1alpha1.ApprovalPhaseExpired,
			}, ctx.Err()

		case <-ticker.C:
			// Check if past our own timeout
			if time.Now().After(deadline) {
				m.expireRequest(ctx, ar)
				return &Result{
					Phase: corev1alpha1.ApprovalPhaseExpired,
				}, nil
			}

			// Fetch current state
			current := &corev1alpha1.ApprovalRequest{}
			if err := m.client.Get(ctx, client.ObjectKeyFromObject(ar), current); err != nil {
				m.log.Error(err, "Failed to poll ApprovalRequest", "name", ar.Name)
				continue
			}

			switch current.Status.Phase {
			case corev1alpha1.ApprovalPhaseApproved:
				m.log.Info("ApprovalRequest APPROVED",
					"name", ar.Name,
					"decidedBy", current.Status.DecidedBy,
				)
				return &Result{
					Approved:  true,
					Phase:     corev1alpha1.ApprovalPhaseApproved,
					DecidedBy: current.Status.DecidedBy,
					Reason:    current.Status.Reason,
				}, nil

			case corev1alpha1.ApprovalPhaseDenied:
				m.log.Info("ApprovalRequest DENIED",
					"name", ar.Name,
					"decidedBy", current.Status.DecidedBy,
					"reason", current.Status.Reason,
				)
				return &Result{
					Approved:  false,
					Phase:     corev1alpha1.ApprovalPhaseDenied,
					DecidedBy: current.Status.DecidedBy,
					Reason:    current.Status.Reason,
				}, nil

			case corev1alpha1.ApprovalPhaseExpired:
				return &Result{
					Phase: corev1alpha1.ApprovalPhaseExpired,
				}, nil
			}
			// Still pending — continue polling
		}
	}
}

// expireRequest marks an ApprovalRequest as expired.
func (m *Manager) expireRequest(ctx context.Context, ar *corev1alpha1.ApprovalRequest) {
	current := &corev1alpha1.ApprovalRequest{}
	if err := m.client.Get(ctx, client.ObjectKeyFromObject(ar), current); err != nil {
		return
	}
	if current.Status.Phase != corev1alpha1.ApprovalPhasePending {
		return // already decided
	}
	current.Status.Phase = corev1alpha1.ApprovalPhaseExpired
	if err := m.client.Status().Update(ctx, current); err != nil {
		m.log.Error(err, "Failed to expire ApprovalRequest", "name", ar.Name)
	}
}

// ApprovalParams holds the parameters for creating an approval request.
type ApprovalParams struct {
	AgentName   string
	RunName     string
	Namespace   string
	Tool        string
	Tier        corev1alpha1.ActionTier
	Target      string
	Description string
	Context     string
	Args        map[string]string
	Timeout     string
	Channels    []string
}

// RequiresTypedConfirmation returns true for high-risk action tiers.
func RequiresTypedConfirmation(tier corev1alpha1.ActionTier) bool {
	return tier == corev1alpha1.ActionTierDestructiveMutation || tier == corev1alpha1.ActionTierDataMutation
}

// IsTypedConfirmationRequired returns true when an approval has typed-confirmation metadata.
func IsTypedConfirmationRequired(ar *corev1alpha1.ApprovalRequest) bool {
	if ar == nil || ar.Annotations == nil {
		return false
	}
	return strings.EqualFold(ar.Annotations[AnnotationTypedConfirmationRequired], "true")
}

// ValidateTypedConfirmation checks provided typed confirmation against approval metadata.
func ValidateTypedConfirmation(ar *corev1alpha1.ApprovalRequest, provided string, now time.Time) error {
	if !IsTypedConfirmationRequired(ar) {
		return nil
	}
	provided = strings.TrimSpace(provided)
	if provided == "" {
		return fmt.Errorf("typed confirmation required")
	}
	expected := strings.TrimSpace(ar.Annotations[AnnotationTypedConfirmationToken])
	if expected == "" {
		return fmt.Errorf("typed confirmation token missing on approval request")
	}
	if provided != expected {
		return fmt.Errorf("typed confirmation mismatch")
	}
	if expRaw := strings.TrimSpace(ar.Annotations[AnnotationTypedConfirmationExpiresAt]); expRaw != "" {
		exp, err := time.Parse(time.RFC3339, expRaw)
		if err != nil {
			return fmt.Errorf("typed confirmation expiry metadata invalid")
		}
		if now.After(exp) {
			return fmt.Errorf("typed confirmation expired")
		}
	}
	return nil
}

func parseApprovalTimeout(raw string) time.Duration {
	timeout := 30 * time.Minute
	if strings.TrimSpace(raw) == "" {
		return timeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return timeout
	}
	return d
}

func generateTypedConfirmationToken() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "CONFIRM-" + strings.ToUpper(hex.EncodeToString(buf)), nil
}

func appendContextInstruction(base, instruction string) string {
	base = strings.TrimSpace(base)
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return base
	}
	if base == "" {
		return instruction
	}
	return base + "\n\n" + instruction
}

// sanitizeLabel makes a string safe for use as a Kubernetes label value.
func sanitizeLabel(s string) string {
	if len(s) > 63 {
		s = s[:63]
	}
	// Replace dots and slashes with hyphens
	result := make([]byte, len(s))
	for i, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			result[i] = c
		} else {
			result[i] = '-'
		}
	}
	return string(result)
}
