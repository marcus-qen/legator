// Package approval implements a risk-gated approval queue for the control plane.
// Commands that exceed the probe's autonomous capability level are held for
// human approval before dispatch. Each request has a TTL; unanswered requests
// expire and are rejected.
package approval

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/marcus-qen/legator/internal/protocol"
)

// Decision is the outcome of an approval request.
type Decision string

const (
	DecisionPending  Decision = "pending"
	DecisionApproved Decision = "approved"
	DecisionDenied   Decision = "denied"
	DecisionExpired  Decision = "expired"
)

// ApprovalRecord captures one distinct approval actor and timestamp.
type ApprovalRecord struct {
	Actor     string    `json:"actor"`
	Timestamp time.Time `json:"timestamp"`
}

// SubmissionOptions controls quorum behavior for submitted approvals.
type SubmissionOptions struct {
	RequireSecondApprover bool
}

// Request is a pending approval item.
type Request struct {
	ID                    string                   `json:"id"`
	WorkspaceID           string                   `json:"workspace_id,omitempty"`
	ProbeID               string                   `json:"probe_id"`
	Command               *protocol.CommandPayload `json:"command"`
	Reason                string                   `json:"reason"`     // why the action was requested
	RiskLevel             string                   `json:"risk_level"` // low/medium/high/critical
	Requester             string                   `json:"requester"`  // who/what initiated (e.g. "llm-task", "api")
	PolicyDecision        string                   `json:"policy_decision,omitempty"`
	PolicyRationale       any                      `json:"policy_rationale,omitempty"`
	RequireSecondApprover bool                     `json:"require_second_approver,omitempty"`
	RequiredApprovals     int                      `json:"required_approvals,omitempty"`
	Approvals             []ApprovalRecord         `json:"approvals,omitempty"`
	Decision              Decision                 `json:"decision"`
	DecidedBy             string                   `json:"decided_by,omitempty"`
	DecidedAt             time.Time                `json:"decided_at,omitempty"`
	CreatedAt             time.Time                `json:"created_at"`
	ExpiresAt             time.Time                `json:"expires_at"`
}

// RequiredApprovalCount returns the approval quorum for this request.
func (r *Request) RequiredApprovalCount() int {
	if r == nil {
		return 1
	}
	if r.RequiredApprovals > 1 {
		return r.RequiredApprovals
	}
	if r.RequireSecondApprover {
		return 2
	}
	return 1
}

// ApproverIDs returns all recorded approver identities in order.
func (r *Request) ApproverIDs() []string {
	if r == nil || len(r.Approvals) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.Approvals))
	for _, approval := range r.Approvals {
		if actor := strings.TrimSpace(approval.Actor); actor != "" {
			out = append(out, actor)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// LatestApproval returns the most recent recorded approval.
func (r *Request) LatestApproval() (ApprovalRecord, bool) {
	if r == nil || len(r.Approvals) == 0 {
		return ApprovalRecord{}, false
	}
	return r.Approvals[len(r.Approvals)-1], true
}

// Queue manages pending approval requests.
type Queue struct {
	mu       sync.RWMutex
	requests map[string]*Request // id → request
	ttl      time.Duration
	maxSize  int
}

// NewQueue creates a new approval queue.
// ttl is the default time-to-live for pending requests.
func NewQueue(ttl time.Duration, maxSize int) *Queue {
	q := &Queue{
		requests: make(map[string]*Request),
		ttl:      ttl,
		maxSize:  maxSize,
	}
	return q
}

// Submit adds a new approval request without policy explainability metadata.
func (q *Queue) Submit(probeID string, cmd *protocol.CommandPayload, reason, riskLevel, requester string) (*Request, error) {
	return q.SubmitWithPolicyDetails(probeID, cmd, reason, riskLevel, requester, "", nil)
}

// SubmitWithWorkspace is like SubmitWithPolicyDetails but also tags the workspace.
func (q *Queue) SubmitWithWorkspace(workspaceID, probeID string, cmd *protocol.CommandPayload, reason, riskLevel, requester, policyDecision string, policyRationale any) (*Request, error) {
	return q.SubmitWithWorkspaceAndOptions(workspaceID, probeID, cmd, reason, riskLevel, requester, policyDecision, policyRationale, SubmissionOptions{})
}

// SubmitWithWorkspaceAndOptions is like SubmitWithWorkspace but accepts quorum options.
func (q *Queue) SubmitWithWorkspaceAndOptions(workspaceID, probeID string, cmd *protocol.CommandPayload, reason, riskLevel, requester, policyDecision string, policyRationale any, options SubmissionOptions) (*Request, error) {
	req, err := q.SubmitWithPolicyDetailsAndOptions(probeID, cmd, reason, riskLevel, requester, policyDecision, policyRationale, options)
	if err != nil {
		return nil, err
	}
	if req != nil && strings.TrimSpace(workspaceID) != "" {
		q.mu.Lock()
		if r, ok := q.requests[req.ID]; ok {
			r.WorkspaceID = strings.TrimSpace(workspaceID)
		}
		q.mu.Unlock()
	}
	return req, nil
}

// SubmitWithPolicyDetails adds a new approval request and stores policy explainability details.
func (q *Queue) SubmitWithPolicyDetails(probeID string, cmd *protocol.CommandPayload, reason, riskLevel, requester, policyDecision string, policyRationale any) (*Request, error) {
	return q.SubmitWithPolicyDetailsAndOptions(probeID, cmd, reason, riskLevel, requester, policyDecision, policyRationale, SubmissionOptions{})
}

// SubmitWithPolicyDetailsAndOptions adds a new approval request and stores policy explainability details.
func (q *Queue) SubmitWithPolicyDetailsAndOptions(probeID string, cmd *protocol.CommandPayload, reason, riskLevel, requester, policyDecision string, policyRationale any, options SubmissionOptions) (*Request, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Evict expired before checking size
	q.evictExpiredLocked()

	if len(q.requests) >= q.maxSize {
		return nil, fmt.Errorf("approval queue full (%d/%d)", len(q.requests), q.maxSize)
	}

	requiredApprovals := 1
	if options.RequireSecondApprover {
		requiredApprovals = 2
	}

	now := time.Now().UTC()
	req := &Request{
		ID:                    uuid.New().String(),
		ProbeID:               probeID,
		Command:               cmd,
		Reason:                reason,
		RiskLevel:             riskLevel,
		Requester:             requester,
		PolicyDecision:        policyDecision,
		PolicyRationale:       policyRationale,
		RequireSecondApprover: options.RequireSecondApprover,
		RequiredApprovals:     requiredApprovals,
		Decision:              DecisionPending,
		CreatedAt:             now,
		ExpiresAt:             now.Add(q.ttl),
	}

	q.requests[req.ID] = req
	return req, nil
}

// Decide records an approval or denial.
func (q *Queue) Decide(id string, decision Decision, decidedBy string) (*Request, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	decidedBy = strings.TrimSpace(decidedBy)
	if decidedBy == "" {
		return nil, fmt.Errorf("decided_by is required")
	}

	req, ok := q.requests[id]
	if !ok {
		return nil, fmt.Errorf("approval request %s not found", id)
	}

	if req.Decision != DecisionPending {
		return nil, fmt.Errorf("request %s already decided: %s", id, req.Decision)
	}

	if time.Now().UTC().After(req.ExpiresAt) {
		req.Decision = DecisionExpired
		return nil, fmt.Errorf("request %s expired at %s", id, req.ExpiresAt.Format(time.RFC3339))
	}

	if decision != DecisionApproved && decision != DecisionDenied {
		return nil, fmt.Errorf("invalid decision %q: must be approved or denied", decision)
	}

	now := time.Now().UTC()
	if decision == DecisionDenied {
		req.Decision = decision
		req.DecidedBy = decidedBy
		req.DecidedAt = now
		return req, nil
	}

	for _, prior := range req.Approvals {
		if strings.EqualFold(strings.TrimSpace(prior.Actor), decidedBy) {
			return nil, fmt.Errorf("approver %q has already approved request %s", decidedBy, id)
		}
	}

	req.Approvals = append(req.Approvals, ApprovalRecord{Actor: decidedBy, Timestamp: now})
	if len(req.Approvals) < req.RequiredApprovalCount() {
		req.Decision = DecisionPending
		return req, nil
	}

	req.Decision = DecisionApproved
	req.DecidedBy = decidedBy
	req.DecidedAt = now

	return req, nil
}

// Get returns a specific request.
func (q *Queue) Get(id string) (*Request, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	req, ok := q.requests[id]
	return req, ok
}

// Pending returns all pending (non-expired) requests, newest first.
func (q *Queue) Pending() []*Request {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.evictExpiredLocked()

	var result []*Request
	for _, req := range q.requests {
		if req.Decision == DecisionPending {
			result = append(result, req)
		}
	}

	// Sort newest first
	sortRequestsByTime(result)
	return result
}

// All returns all requests (including decided/expired), newest first, limited.
func (q *Queue) All(limit int) []*Request {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var result []*Request
	for _, req := range q.requests {
		result = append(result, req)
	}

	sortRequestsByTime(result)

	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result
}

// PendingCount returns the number of pending requests.
func (q *Queue) PendingCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.evictExpiredLocked()
	count := 0
	for _, req := range q.requests {
		if req.Decision == DecisionPending {
			count++
		}
	}
	return count
}

// StartReaper runs a background goroutine that expires stale requests.
func (q *Queue) StartReaper(interval time.Duration, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				q.mu.Lock()
				q.evictExpiredLocked()
				q.mu.Unlock()
			}
		}
	}()
}

// WaitForDecision blocks until the request is decided or context expires.
func (q *Queue) WaitForDecision(id string, timeout time.Duration) (*Request, error) {
	deadline := time.Now().Add(timeout)
	pollInterval := 500 * time.Millisecond

	for {
		req, ok := q.Get(id)
		if !ok {
			return nil, fmt.Errorf("request %s not found", id)
		}

		switch req.Decision {
		case DecisionApproved, DecisionDenied, DecisionExpired:
			return req, nil
		}

		if time.Now().After(deadline) {
			return req, fmt.Errorf("timeout waiting for decision on %s", id)
		}

		time.Sleep(pollInterval)
	}
}

func (q *Queue) evictExpiredLocked() {
	now := time.Now().UTC()
	for id, req := range q.requests {
		if req.Decision == DecisionPending && now.After(req.ExpiresAt) {
			req.Decision = DecisionExpired
			// Keep expired requests for audit trail; purge old decided
			_ = id
		}
	}

	// Purge decided requests older than 24h to prevent unbounded growth
	cutoff := now.Add(-24 * time.Hour)
	for id, req := range q.requests {
		if req.Decision != DecisionPending && req.CreatedAt.Before(cutoff) {
			delete(q.requests, id)
		}
	}
}

// sortRequestsByTime sorts newest first. Uses sort inline to avoid import.
func sortRequestsByTime(reqs []*Request) {
	// Simple insertion sort — queue is small
	for i := 1; i < len(reqs); i++ {
		for j := i; j > 0 && reqs[j].CreatedAt.After(reqs[j-1].CreatedAt); j-- {
			reqs[j], reqs[j-1] = reqs[j-1], reqs[j]
		}
	}
}

// PendingByWorkspace returns all pending (non-expired) requests for a specific workspace.
// When workspaceID is empty it returns all pending requests regardless of workspace.
func (q *Queue) PendingByWorkspace(workspaceID string) []*Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.evictExpiredLocked()
	var result []*Request
	for _, req := range q.requests {
		if req.Decision != DecisionPending {
			continue
		}
		if workspaceID != "" && req.WorkspaceID != "" && req.WorkspaceID != workspaceID {
			continue
		}
		result = append(result, req)
	}
	sortRequestsByTime(result)
	return result
}

// AllByWorkspace returns all requests for a workspace, newest first, up to limit.
// When workspaceID is empty it returns all requests regardless of workspace.
func (q *Queue) AllByWorkspace(workspaceID string, limit int) []*Request {
	q.mu.RLock()
	defer q.mu.RUnlock()
	var result []*Request
	for _, req := range q.requests {
		if workspaceID != "" && req.WorkspaceID != "" && req.WorkspaceID != workspaceID {
			continue
		}
		result = append(result, req)
	}
	sortRequestsByTime(result)
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result
}

// GetCheckWorkspace fetches a request and returns (nil, false) if it belongs
// to a different workspace. When expectedWorkspace is empty no check is performed.
func (q *Queue) GetCheckWorkspace(id, expectedWorkspace string) (*Request, bool) {
	req, ok := q.Get(id)
	if !ok {
		return nil, false
	}
	if expectedWorkspace != "" && req.WorkspaceID != "" && req.WorkspaceID != expectedWorkspace {
		return nil, false
	}
	return req, true
}

// ClassifyRisk returns a risk level based on command intent.
// Heuristic rules are intentionally conservative: anything that mutates system
// state is high (approval required); clearly destructive actions are critical.
func ClassifyRisk(cmd *protocol.CommandPayload) string {
	line := strings.TrimSpace(strings.ToLower(strings.Join(append([]string{cmd.Command}, cmd.Args...), " ")))
	if line == "" {
		return "medium"
	}

	if line == "rm" || strings.HasPrefix(line, "rm -") || line == "dd" || strings.HasPrefix(line, "dd if=") {
		return "critical"
	}
	criticalPrefixes := []string{
		"mkfs", "fdisk", "parted", "shutdown", "reboot", "init 0", "init 6",
		"poweroff", "iptables", "nft flush", "userdel",
	}
	for _, p := range criticalPrefixes {
		if strings.HasPrefix(line, p) {
			return "critical"
		}
	}

	highPrefixes := []string{
		"systemctl restart", "systemctl stop", "systemctl start", "service ",
		"apt install", "apt remove", "apt upgrade", "apt-get install", "apt-get remove", "apt-get upgrade",
		"yum install", "yum remove", "dnf install", "dnf remove",
		"pip install", "npm install", "npm uninstall",
		"chmod", "chown", "mv ", "cp ", "tee ", "sed -i", "truncate",
	}
	for _, p := range highPrefixes {
		if strings.HasPrefix(line, p) {
			return "high"
		}
	}

	mediumPrefixes := []string{
		"journalctl", "dmesg", "ss ", "netstat", "lsof", "du ", "find ",
		"grep ", "awk ", "ps ", "top", "systemctl status", "ip ", "route",
	}
	for _, p := range mediumPrefixes {
		if strings.HasPrefix(line, p) {
			return "medium"
		}
	}

	lowPrefixes := []string{
		"ls", "cat", "head", "tail", "pwd", "whoami", "id", "uname", "hostname", "uptime", "df", "free", "echo ", "echo",
	}
	for _, p := range lowPrefixes {
		if strings.HasPrefix(line, p) {
			return "low"
		}
	}

	// Fallback by declared level if command is unknown.
	switch cmd.Level {
	case protocol.CapObserve:
		return "low"
	case protocol.CapDiagnose:
		return "medium"
	default:
		return "high"
	}
}

// NeedsApproval returns true if a command requires human approval before dispatch.
func NeedsApproval(cmd *protocol.CommandPayload, probeLevel protocol.CapabilityLevel) bool {
	risk := ClassifyRisk(cmd)
	return risk == "high" || risk == "critical"
}
