// Package executor - streaming command execution support.
package executor

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

// ChunkCallback is called for each output chunk during streaming execution.
type ChunkCallback func(chunk protocol.OutputChunkPayload)

// ExecuteStream runs a command and streams output chunks via the callback.
// It calls the callback for each line of stdout/stderr, then sends a final chunk.
// Policy checks are the same as Execute.
func (e *Executor) ExecuteStream(ctx context.Context, cmd *protocol.CommandPayload, cb ChunkCallback) {
	// Policy checks (same as Execute)
	requiredLevel := e.effectiveLevel(cmd)
	if !e.levelAllowed(requiredLevel) {
		cb(protocol.OutputChunkPayload{
			RequestID: cmd.RequestID,
			Stream:    "stderr",
			Data: "policy violation: command classified as " + string(requiredLevel) +
				" but probe is at " + string(e.policy.Level) + " level",
			Final:    true,
			ExitCode: -1,
		})
		return
	}

	fullCmd := cmd.Command
	if len(cmd.Args) > 0 {
		fullCmd = cmd.Command + " " + strings.Join(cmd.Args, " ")
	}

	if e.isBlocked(fullCmd) {
		cb(protocol.OutputChunkPayload{
			RequestID: cmd.RequestID,
			Stream:    "stderr",
			Data:      "policy violation: command is blocked",
			Final:     true,
			ExitCode:  -1,
		})
		return
	}

	if len(e.policy.Allowed) > 0 && !e.isAllowed(fullCmd) {
		cb(protocol.OutputChunkPayload{
			RequestID: cmd.RequestID,
			Stream:    "stderr",
			Data:      "policy violation: command not in allowlist",
			Final:     true,
			ExitCode:  -1,
		})
		return
	}

	timeout := cmd.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	spec, err := buildExecSpec(cmd)
	if err != nil {
		cb(protocol.OutputChunkPayload{
			RequestID: cmd.RequestID,
			Stream:    "stderr",
			Data:      err.Error(),
			Final:     true,
			ExitCode:  -1,
		})
		return
	}

	c := exec.CommandContext(execCtx, spec.name, spec.args...)

	stdout, err := c.StdoutPipe()
	if err != nil {
		cb(protocol.OutputChunkPayload{
			RequestID: cmd.RequestID,
			Stream:    "stderr",
			Data:      "failed to create stdout pipe: " + err.Error(),
			Final:     true,
			ExitCode:  -1,
		})
		return
	}

	stderr, err := c.StderrPipe()
	if err != nil {
		cb(protocol.OutputChunkPayload{
			RequestID: cmd.RequestID,
			Stream:    "stderr",
			Data:      "failed to create stderr pipe: " + err.Error(),
			Final:     true,
			ExitCode:  -1,
		})
		return
	}

	start := time.Now()
	if err := c.Start(); err != nil {
		cb(protocol.OutputChunkPayload{
			RequestID: cmd.RequestID,
			Stream:    "stderr",
			Data:      "failed to start command: " + err.Error(),
			Final:     true,
			ExitCode:  -1,
		})
		return
	}

	var seq atomic.Int32
	var wg sync.WaitGroup
	wg.Add(2)

	streamPipe := func(r io.Reader, stream string) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 64*1024), maxOutputSize)
		for scanner.Scan() {
			cb(protocol.OutputChunkPayload{
				RequestID: cmd.RequestID,
				Stream:    stream,
				Data:      scanner.Text() + "\n",
				Seq:       int(seq.Add(1)),
			})
		}
	}

	go streamPipe(stdout, "stdout")
	go streamPipe(stderr, "stderr")

	wg.Wait()

	exitCode := 0
	if err := c.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	duration := time.Since(start).Milliseconds()

	// Send final chunk
	cb(protocol.OutputChunkPayload{
		RequestID: cmd.RequestID,
		Stream:    "stdout",
		Data:      "",
		Seq:       int(seq.Add(1)),
		Final:     true,
		ExitCode:  exitCode,
	})

	e.logger.Info("streaming command completed",
		zap.String("request_id", cmd.RequestID),
		zap.String("command", cmd.Command),
		zap.Int("exit_code", exitCode),
		zap.Int64("duration_ms", duration),
	)
}
