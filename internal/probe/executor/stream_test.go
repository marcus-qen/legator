package executor

import (
	"context"
	"sync"
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func TestExecuteStream_PolicyBlocked(t *testing.T) {
	e := New(Policy{Level: protocol.CapObserve}, zap.NewNop())
	cmd := &protocol.CommandPayload{
		RequestID: "s1",
		Command:   "rm",
		Args:      []string{"-rf", "/"},
		Level:     protocol.CapRemediate,
	}

	var chunks []protocol.OutputChunkPayload
	e.ExecuteStream(context.Background(), cmd, func(c protocol.OutputChunkPayload) {
		chunks = append(chunks, c)
	})

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if !chunks[0].Final {
		t.Fatal("expected final chunk")
	}
	if chunks[0].ExitCode != -1 {
		t.Fatalf("expected exit -1, got %d", chunks[0].ExitCode)
	}
}

func TestExecuteStream_BlockedCommand(t *testing.T) {
	e := New(Policy{
		Level:   protocol.CapRemediate,
		Blocked: []string{"rm"},
	}, zap.NewNop())
	cmd := &protocol.CommandPayload{
		RequestID: "s2",
		Command:   "rm",
		Args:      []string{"-rf", "/tmp/test"},
		Level:     protocol.CapRemediate,
	}

	var chunks []protocol.OutputChunkPayload
	e.ExecuteStream(context.Background(), cmd, func(c protocol.OutputChunkPayload) {
		chunks = append(chunks, c)
	})

	if len(chunks) != 1 || !chunks[0].Final {
		t.Fatal("expected single final chunk for blocked command")
	}
	if chunks[0].ExitCode != -1 {
		t.Fatal("expected exit -1")
	}
}

func TestExecuteStream_EchoStreams(t *testing.T) {
	e := New(Policy{Level: protocol.CapObserve}, zap.NewNop())
	cmd := &protocol.CommandPayload{
		RequestID: "s3",
		Command:   "echo",
		Args:      []string{"hello streaming"},
		Level:     protocol.CapObserve,
	}

	var mu sync.Mutex
	var chunks []protocol.OutputChunkPayload
	e.ExecuteStream(context.Background(), cmd, func(c protocol.OutputChunkPayload) {
		mu.Lock()
		chunks = append(chunks, c)
		mu.Unlock()
	})

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks (data + final), got %d", len(chunks))
	}

	// Last chunk should be final
	last := chunks[len(chunks)-1]
	if !last.Final {
		t.Fatal("last chunk should be final")
	}
	if last.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", last.ExitCode)
	}

	// Should have stdout data
	gotOutput := false
	for _, c := range chunks {
		if c.Stream == "stdout" && c.Data != "" {
			gotOutput = true
		}
	}
	if !gotOutput {
		t.Fatal("expected stdout output")
	}
}

func TestExecuteStream_MultilineOutput(t *testing.T) {
	e := New(Policy{Level: protocol.CapObserve}, zap.NewNop())
	cmd := &protocol.CommandPayload{
		RequestID: "s4",
		Command:   "sh",
		Args:      []string{"-c", "echo line1; echo line2; echo line3"},
		Level:     protocol.CapObserve,
	}

	var mu sync.Mutex
	var chunks []protocol.OutputChunkPayload
	e.ExecuteStream(context.Background(), cmd, func(c protocol.OutputChunkPayload) {
		mu.Lock()
		chunks = append(chunks, c)
		mu.Unlock()
	})

	// Count stdout data chunks (excluding final)
	dataChunks := 0
	for _, c := range chunks {
		if c.Stream == "stdout" && c.Data != "" && !c.Final {
			dataChunks++
		}
	}
	if dataChunks < 3 {
		t.Fatalf("expected at least 3 data chunks for 3 lines, got %d", dataChunks)
	}

	// Verify sequence numbers are ascending
	for i := 1; i < len(chunks); i++ {
		if chunks[i].Seq <= chunks[i-1].Seq {
			t.Fatalf("sequence not ascending: %d -> %d", chunks[i-1].Seq, chunks[i].Seq)
		}
	}
}

func TestExecuteStream_StderrOutput(t *testing.T) {
	e := New(Policy{Level: protocol.CapObserve}, zap.NewNop())
	cmd := &protocol.CommandPayload{
		RequestID: "s5",
		Command:   "sh",
		Args:      []string{"-c", "echo error >&2"},
		Level:     protocol.CapObserve,
	}

	var mu sync.Mutex
	var chunks []protocol.OutputChunkPayload
	e.ExecuteStream(context.Background(), cmd, func(c protocol.OutputChunkPayload) {
		mu.Lock()
		chunks = append(chunks, c)
		mu.Unlock()
	})

	gotStderr := false
	for _, c := range chunks {
		if c.Stream == "stderr" && c.Data != "" {
			gotStderr = true
		}
	}
	if !gotStderr {
		t.Fatal("expected stderr output")
	}
}

func TestExecuteStream_NonZeroExit(t *testing.T) {
	e := New(Policy{Level: protocol.CapObserve}, zap.NewNop())
	cmd := &protocol.CommandPayload{
		RequestID: "s6",
		Command:   "sh",
		Args:      []string{"-c", "exit 42"},
		Level:     protocol.CapObserve,
	}

	var mu sync.Mutex
	var chunks []protocol.OutputChunkPayload
	e.ExecuteStream(context.Background(), cmd, func(c protocol.OutputChunkPayload) {
		mu.Lock()
		chunks = append(chunks, c)
		mu.Unlock()
	})

	last := chunks[len(chunks)-1]
	if !last.Final {
		t.Fatal("last chunk should be final")
	}
	if last.ExitCode != 42 {
		t.Fatalf("expected exit 42, got %d", last.ExitCode)
	}
}
