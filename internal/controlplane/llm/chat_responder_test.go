package llm

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

// mockProvider returns canned responses.
type mockProvider struct {
	responses []string
	callIdx   int
}

func (m *mockProvider) Name() string { return "mock" }
func (m *mockProvider) Complete(_ context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	if m.callIdx >= len(m.responses) {
		return &CompletionResponse{Content: "Done."}, nil
	}
	resp := m.responses[m.callIdx]
	m.callIdx++
	return &CompletionResponse{Content: resp, Model: "mock-model"}, nil
}

func TestChatResponder_ConversationalReply(t *testing.T) {
	mp := &mockProvider{responses: []string{"The server is running Linux with 4 CPUs."}}

	cr := NewChatResponder(mp, nil, zap.NewNop())
	reply, err := cr.Respond(
		context.Background(),
		"test-probe",
		nil,
		"What OS is this server running?",
		&protocol.InventoryPayload{
			Hostname: "web-01",
			OS:       "linux",
			Arch:     "amd64",
			Kernel:   "6.1.0",
			CPUs:     4,
			MemTotal: 8 * 1024 * 1024 * 1024,
		},
		protocol.CapObserve,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "The server is running Linux with 4 CPUs." {
		t.Fatalf("unexpected reply: %s", reply)
	}
}

func TestChatResponder_CommandExecution(t *testing.T) {
	cmdJSON, _ := json.Marshal(CommandRequest{
		Command: "uptime",
		Args:    nil,
		Reason:  "checking system uptime",
	})

	mp := &mockProvider{
		responses: []string{
			string(cmdJSON),
			"The server has been up for 42 days.",
		},
	}

	dispatch := func(probeID string, cmd *protocol.CommandPayload) (*protocol.CommandResultPayload, error) {
		if cmd.Command != "uptime" {
			t.Fatalf("unexpected command: %s", cmd.Command)
		}
		return &protocol.CommandResultPayload{
			RequestID: cmd.RequestID,
			ExitCode:  0,
			Stdout:    " 14:23:01 up 42 days,  3:15,  2 users,  load average: 0.15, 0.10, 0.05",
			Duration:  5,
		}, nil
	}

	cr := NewChatResponder(mp, dispatch, zap.NewNop())
	reply, err := cr.Respond(
		context.Background(),
		"test-probe",
		nil,
		"How long has this server been running?",
		nil,
		protocol.CapObserve,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "The server has been up for 42 days." {
		t.Fatalf("unexpected reply: %s", reply)
	}
	if mp.callIdx != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", mp.callIdx)
	}
}

func TestChatResponder_WithHistory(t *testing.T) {
	mp := &mockProvider{responses: []string{"Based on our earlier discussion, yes the disk is nearly full."}}

	history := []ChatMessage{
		{Role: "user", Content: "Check disk usage"},
		{Role: "assistant", Content: "The disk is at 92% capacity."},
	}

	cr := NewChatResponder(mp, nil, zap.NewNop())
	reply, err := cr.Respond(
		context.Background(),
		"test-probe",
		history,
		"Should I be worried?",
		nil,
		protocol.CapObserve,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reply == "" {
		t.Fatal("empty reply")
	}
}
