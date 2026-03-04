package discovery

import (
	"errors"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

// mockSender records sent messages.
type mockSender struct {
	messages []sentMessage
	sendErr  error
}

type sentMessage struct {
	msgType protocol.MessageType
	payload any
}

func (m *mockSender) Send(msgType protocol.MessageType, payload any) error {
	if m.sendErr != nil {
		return m.sendErr
	}
	m.messages = append(m.messages, sentMessage{msgType, payload})
	return nil
}

func TestReporterReport(t *testing.T) {
	sender := &mockSender{}
	reporter := NewReporter()

	hosts := []SSHHost{
		{IP: "10.0.0.1", Port: 22, SSHBanner: "SSH-2.0-OpenSSH_8.4", OSGuess: "linux"},
		{IP: "10.0.0.2", Port: 22, SSHBanner: "SSH-2.0-Dropbear"},
	}

	n, err := reporter.Report(sender, "probe-1", hosts)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 new hosts, got %d", n)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("expected 1 message sent, got %d", len(sender.messages))
	}

	msg := sender.messages[0]
	if msg.msgType != protocol.MsgDiscoveryReport {
		t.Fatalf("expected MsgDiscoveryReport, got %q", msg.msgType)
	}

	payload := msg.payload.(protocol.DiscoveryReportPayload)
	if payload.ProbeID != "probe-1" {
		t.Fatalf("expected probe-1, got %q", payload.ProbeID)
	}
	if len(payload.Hosts) != 2 {
		t.Fatalf("expected 2 hosts in payload, got %d", len(payload.Hosts))
	}
}

func TestReporterDedup(t *testing.T) {
	sender := &mockSender{}
	reporter := NewReporter()

	hosts := []SSHHost{
		{IP: "10.0.0.1", Port: 22},
	}

	// First report — should send.
	n1, err := reporter.Report(sender, "probe-1", hosts)
	if err != nil || n1 != 1 {
		t.Fatalf("first report: n=%d err=%v", n1, err)
	}

	// Second report of the same host — should be deduplicated.
	n2, err := reporter.Report(sender, "probe-1", hosts)
	if err != nil {
		t.Fatalf("second report error: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("expected 0 new hosts on dedup, got %d", n2)
	}
	// Only 1 message should have been sent.
	if len(sender.messages) != 1 {
		t.Fatalf("expected 1 total message, got %d", len(sender.messages))
	}
}

func TestReporterDedupPartial(t *testing.T) {
	sender := &mockSender{}
	reporter := NewReporter()

	hosts1 := []SSHHost{{IP: "10.0.0.1", Port: 22}, {IP: "10.0.0.2", Port: 22}}
	reporter.Report(sender, "probe-1", hosts1) //nolint:errcheck

	// Third host is new, first two are known.
	hosts2 := []SSHHost{
		{IP: "10.0.0.1", Port: 22},
		{IP: "10.0.0.2", Port: 22},
		{IP: "10.0.0.3", Port: 22},
	}
	n, err := reporter.Report(sender, "probe-1", hosts2)
	if err != nil {
		t.Fatalf("partial report: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 new host, got %d", n)
	}

	payload := sender.messages[1].payload.(protocol.DiscoveryReportPayload)
	if len(payload.Hosts) != 1 || payload.Hosts[0].IP != "10.0.0.3" {
		t.Fatalf("expected only 10.0.0.3 in second report, got %v", payload.Hosts)
	}
}

func TestReporterReset(t *testing.T) {
	sender := &mockSender{}
	reporter := NewReporter()

	hosts := []SSHHost{{IP: "10.0.0.1", Port: 22}}
	reporter.Report(sender, "probe-1", hosts) //nolint:errcheck

	reporter.Reset()

	// After reset, same host should be reported again.
	n, err := reporter.Report(sender, "probe-1", hosts)
	if err != nil {
		t.Fatalf("after reset: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 new host after reset, got %d", n)
	}
}

func TestReporterEmptyHosts(t *testing.T) {
	sender := &mockSender{}
	reporter := NewReporter()

	n, err := reporter.Report(sender, "probe-1", nil)
	if err != nil {
		t.Fatalf("empty hosts: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}
	if len(sender.messages) != 0 {
		t.Fatal("expected no messages for empty host list")
	}
}

func TestReporterSendError(t *testing.T) {
	sender := &mockSender{sendErr: errors.New("connection lost")}
	reporter := NewReporter()

	hosts := []SSHHost{{IP: "10.0.0.1", Port: 22}}
	_, err := reporter.Report(sender, "probe-1", hosts)
	if err == nil {
		t.Fatal("expected error from sender")
	}
}

func TestReporterPayloadTimestamp(t *testing.T) {
	sender := &mockSender{}
	reporter := NewReporter()

	before := time.Now().UTC()
	reporter.Report(sender, "p", []SSHHost{{IP: "1.2.3.4", Port: 22}}) //nolint:errcheck
	after := time.Now().UTC()

	payload := sender.messages[0].payload.(protocol.DiscoveryReportPayload)
	if payload.ScannedAt.Before(before) || payload.ScannedAt.After(after) {
		t.Errorf("ScannedAt out of range: %v", payload.ScannedAt)
	}
}

func TestReporterDifferentPorts(t *testing.T) {
	sender := &mockSender{}
	reporter := NewReporter()

	hosts := []SSHHost{
		{IP: "10.0.0.1", Port: 22},
		{IP: "10.0.0.1", Port: 2222}, // same IP, different port → different key
	}

	n, err := reporter.Report(sender, "probe-1", hosts)
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 new hosts (different ports), got %d", n)
	}
}
