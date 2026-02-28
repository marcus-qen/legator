package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	corecommanddispatch "github.com/marcus-qen/legator/internal/controlplane/core/commanddispatch"
	"github.com/marcus-qen/legator/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpStubSender struct {
	sendFn func(probeID string, msgType protocol.MessageType, payload any) error
}

func (s *mcpStubSender) SendTo(probeID string, msgType protocol.MessageType, payload any) error {
	if s.sendFn != nil {
		return s.sendFn(probeID, msgType, payload)
	}
	return nil
}

func TestHandleRunCommand_Success(t *testing.T) {
	srv, fleetStore, _, _ := newTestMCPServer(t)
	fleetStore.Register("probe-run", "host-run", "linux", "amd64")

	tracker := cmdtracker.New(time.Minute)
	srv.dispatcher = corecommanddispatch.NewService(&mcpStubSender{sendFn: func(_ string, _ protocol.MessageType, payload any) error {
		cmd, ok := payload.(protocol.CommandPayload)
		if !ok {
			t.Fatalf("expected protocol.CommandPayload, got %T", payload)
		}
		go func() {
			_ = tracker.Complete(cmd.RequestID, &protocol.CommandResultPayload{RequestID: cmd.RequestID, ExitCode: 0, Stdout: " ok "})
		}()
		return nil
	}}, tracker)

	result, _, err := srv.handleRunCommand(context.Background(), nil, runCommandInput{ProbeID: "probe-run", Command: "echo ok"})
	if err != nil {
		t.Fatalf("handleRunCommand returned error: %v", err)
	}
	if got := toolText(t, result); got != "ok" {
		t.Fatalf("unexpected tool text: %q", got)
	}
}

func TestHandleRunCommand_DispatchErrorWrapped(t *testing.T) {
	srv, fleetStore, _, _ := newTestMCPServer(t)
	fleetStore.Register("probe-offline", "host-offline", "linux", "amd64")

	tracker := cmdtracker.New(time.Minute)
	srv.dispatcher = corecommanddispatch.NewService(&mcpStubSender{sendFn: func(_ string, _ protocol.MessageType, _ any) error {
		return fmt.Errorf("not connected")
	}}, tracker)

	_, _, err := srv.handleRunCommand(context.Background(), nil, runCommandInput{ProbeID: "probe-offline", Command: "hostname"})
	if err == nil {
		t.Fatal("expected dispatch error")
	}
	if !strings.Contains(err.Error(), "dispatch command: not connected") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleRunCommand_ContextDeadlinePassthrough(t *testing.T) {
	srv, fleetStore, _, _ := newTestMCPServer(t)
	fleetStore.Register("probe-slow", "host-slow", "linux", "amd64")

	tracker := cmdtracker.New(time.Minute)
	srv.dispatcher = corecommanddispatch.NewService(&mcpStubSender{}, tracker)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, _, err := srv.handleRunCommand(ctx, nil, runCommandInput{ProbeID: "probe-slow", Command: "sleep 1"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
}

func toolText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if result == nil || len(result.Content) == 0 {
		t.Fatalf("empty tool result: %#v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", result.Content[0])
	}
	return text.Text
}
