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

// Request is a pending approval item.
type Request struct {
	ID        string                   `json:"id"`
	ProbeID   string                   `json:"probe_id"`
	Command   *protocol.CommandPayload `json:"command"`
	Reason    string                   `json:"reason"`     // why the action was requested
	RiskLevel string                   `json:"risk_level"` // low/medium/high/critical
	Requester string                   `json:"requester"`  // who/what initiated (e.g. "llm-task", "api")
	Decision  Decision                 `json:"decision"`
	DecidedBy string                   `json:"decided_by,omitempty"`
	DecidedAt time.Time                `json:"decided_at,omitempty"`
	CreatedAt time.Time                `json:"created_at"`
	ExpiresAt time.Time                `json:"expires_at"`
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

// Submit adds a new approval request. Returns the request ID.
func (q *Queue) Submit(probeID string, cmd *protocol.CommandPayload, reason, riskLevel, requester string) (*Request, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Evict expired before checking size
	q.evictExpiredLocked()

	if len(q.requests) >= q.maxSize {
		return nil, fmt.Errorf("approval queue full (%d/%d)", len(q.requests), q.maxSize)
	}

	req := &Request{
		ID:        uuid.New().String(),
		ProbeID:   probeID,
		Command:   cmd,
		Reason:    reason,
		RiskLevel: riskLevel,
		Requester: requester,
		Decision:  DecisionPending,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(q.ttl),
	}

	q.requests[req.ID] = req
	return req, nil
}

// Decide records an approval or denial.
func (q *Queue) Decide(id string, decision Decision, decidedBy string) (*Request, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

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

	req.Decision = decision
	req.DecidedBy = decidedBy
	req.DecidedAt = time.Now().UTC()

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
