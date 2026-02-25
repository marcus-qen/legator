package approval

import (
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

func makeCmd(command string, level protocol.CapabilityLevel) *protocol.CommandPayload {
	return &protocol.CommandPayload{
		RequestID: "test-123",
		Command:   command,
		Args:      nil,
		Level:     level,
		Timeout:   10 * time.Second,
	}
}

func TestSubmitAndGet(t *testing.T) {
	q := NewQueue(5*time.Minute, 100)
	cmd := makeCmd("systemctl restart nginx", protocol.CapRemediate)

	req, err := q.Submit("probe-1", cmd, "nginx is unresponsive", "high", "llm-task")
	if err != nil {
		t.Fatal(err)
	}
	if req.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if req.Decision != DecisionPending {
		t.Fatalf("expected pending, got %s", req.Decision)
	}

	got, ok := q.Get(req.ID)
	if !ok {
		t.Fatal("expected to find request")
	}
	if got.ProbeID != "probe-1" {
		t.Fatalf("expected probe-1, got %s", got.ProbeID)
	}
}

func TestApprove(t *testing.T) {
	q := NewQueue(5*time.Minute, 100)
	cmd := makeCmd("apt-get upgrade", protocol.CapRemediate)

	req, _ := q.Submit("probe-2", cmd, "security patches", "high", "api")
	decided, err := q.Decide(req.ID, DecisionApproved, "keith")
	if err != nil {
		t.Fatal(err)
	}
	if decided.Decision != DecisionApproved {
		t.Fatalf("expected approved, got %s", decided.Decision)
	}
	if decided.DecidedBy != "keith" {
		t.Fatalf("expected keith, got %s", decided.DecidedBy)
	}
}

func TestDeny(t *testing.T) {
	q := NewQueue(5*time.Minute, 100)
	cmd := makeCmd("rm -rf /tmp/data", protocol.CapRemediate)

	req, _ := q.Submit("probe-3", cmd, "cleanup", "critical", "llm-task")
	decided, err := q.Decide(req.ID, DecisionDenied, "keith")
	if err != nil {
		t.Fatal(err)
	}
	if decided.Decision != DecisionDenied {
		t.Fatalf("expected denied, got %s", decided.Decision)
	}
}

func TestExpiry(t *testing.T) {
	q := NewQueue(50*time.Millisecond, 100)
	cmd := makeCmd("reboot", protocol.CapRemediate)

	req, _ := q.Submit("probe-4", cmd, "reboot needed", "critical", "api")

	time.Sleep(100 * time.Millisecond)

	// Trying to decide should fail with expiry
	_, err := q.Decide(req.ID, DecisionApproved, "keith")
	if err == nil {
		t.Fatal("expected error for expired request")
	}

	got, ok := q.Get(req.ID)
	if !ok {
		t.Fatal("expected to find expired request")
	}
	if got.Decision != DecisionExpired {
		t.Fatalf("expected expired, got %s", got.Decision)
	}
}

func TestDoubleDecide(t *testing.T) {
	q := NewQueue(5*time.Minute, 100)
	cmd := makeCmd("systemctl start app", protocol.CapRemediate)

	req, _ := q.Submit("probe-5", cmd, "start app", "high", "api")
	_, err := q.Decide(req.ID, DecisionApproved, "keith")
	if err != nil {
		t.Fatal(err)
	}

	_, err = q.Decide(req.ID, DecisionDenied, "someone-else")
	if err == nil {
		t.Fatal("expected error for double-decide")
	}
}

func TestPendingList(t *testing.T) {
	q := NewQueue(5*time.Minute, 100)

	q.Submit("p1", makeCmd("cmd1", protocol.CapRemediate), "reason1", "high", "api")
	q.Submit("p2", makeCmd("cmd2", protocol.CapRemediate), "reason2", "high", "api")
	req3, _ := q.Submit("p3", makeCmd("cmd3", protocol.CapRemediate), "reason3", "high", "api")

	// Approve one
	q.Decide(req3.ID, DecisionApproved, "keith")

	pending := q.Pending()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}
}

func TestQueueFull(t *testing.T) {
	q := NewQueue(5*time.Minute, 2)

	q.Submit("p1", makeCmd("cmd1", protocol.CapRemediate), "r", "high", "api")
	q.Submit("p2", makeCmd("cmd2", protocol.CapRemediate), "r", "high", "api")

	_, err := q.Submit("p3", makeCmd("cmd3", protocol.CapRemediate), "r", "high", "api")
	if err == nil {
		t.Fatal("expected queue full error")
	}
}

func TestClassifyRisk(t *testing.T) {
	tests := []struct {
		cmd      string
		level    protocol.CapabilityLevel
		expected string
	}{
		{"ls", protocol.CapObserve, "low"},
		{"df", protocol.CapDiagnose, "low"},
		{"systemctl restart nginx", protocol.CapRemediate, "high"},
		{"rm", protocol.CapRemediate, "critical"},
		{"reboot", protocol.CapRemediate, "critical"},
		{"dd", protocol.CapRemediate, "critical"},
	}

	for _, tt := range tests {
		cmd := makeCmd(tt.cmd, tt.level)
		got := ClassifyRisk(cmd)
		if got != tt.expected {
			t.Errorf("ClassifyRisk(%q, %s) = %s, want %s", tt.cmd, tt.level, got, tt.expected)
		}
	}
}

func TestNeedsApproval(t *testing.T) {
	// Observe commands don't need approval
	cmd := makeCmd("ls", protocol.CapObserve)
	if NeedsApproval(cmd, protocol.CapObserve) {
		t.Error("observe commands should not need approval")
	}

	// High-risk remediate commands need approval
	cmd = makeCmd("apt-get upgrade", protocol.CapRemediate)
	if !NeedsApproval(cmd, protocol.CapRemediate) {
		t.Error("remediate commands should need approval")
	}

	// Critical commands always need approval
	cmd = makeCmd("rm", protocol.CapRemediate)
	if !NeedsApproval(cmd, protocol.CapRemediate) {
		t.Error("critical commands should need approval")
	}
}

func TestWaitForDecisionApproved(t *testing.T) {
	q := NewQueue(5*time.Minute, 100)
	cmd := makeCmd("systemctl restart nginx", protocol.CapRemediate)

	req, _ := q.Submit("probe-1", cmd, "restart", "high", "llm-task")

	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = q.Decide(req.ID, DecisionApproved, "keith")
	}()

	decided, err := q.WaitForDecision(req.ID, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if decided.Decision != DecisionApproved {
		t.Fatalf("expected approved, got %s", decided.Decision)
	}
}

func TestWaitForDecisionTimeout(t *testing.T) {
	q := NewQueue(5*time.Minute, 100)
	cmd := makeCmd("systemctl restart nginx", protocol.CapRemediate)

	req, _ := q.Submit("probe-1", cmd, "restart", "high", "llm-task")

	_, err := q.WaitForDecision(req.ID, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}

	current, ok := q.Get(req.ID)
	if !ok {
		t.Fatal("request disappeared")
	}
	if current.Decision != DecisionPending {
		t.Fatalf("expected still pending after timeout, got %s", current.Decision)
	}
}
