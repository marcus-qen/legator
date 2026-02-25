// Package executor runs commands on the probe's host with local policy enforcement.
package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

const (
	maxOutputSize = 1 << 20 // 1MB per stream
	defaultTimeout = 30 * time.Second
)

// Policy defines what the executor is allowed to do.
type Policy struct {
	Level   protocol.CapabilityLevel
	Allowed []string // command prefixes allowed (empty = all at level)
	Blocked []string // command prefixes always blocked
	Paths   []string // protected paths (no writes)
}

// Executor runs commands with policy enforcement.
type Executor struct {
	policy Policy
	logger *zap.Logger
}

// New creates an executor with the given policy.
func New(policy Policy, logger *zap.Logger) *Executor {
	return &Executor{
		policy: policy,
		logger: logger,
	}
}

// Execute runs a command if policy allows it.
func (e *Executor) Execute(ctx context.Context, cmd *protocol.CommandPayload) *protocol.CommandResultPayload {
	result := &protocol.CommandResultPayload{
		RequestID: cmd.RequestID,
	}

	// Policy check: capability level
	if !e.levelAllowed(cmd.Level) {
		result.ExitCode = -1
		result.Stderr = fmt.Sprintf("policy violation: command requires %s but probe is at %s level",
			cmd.Level, e.policy.Level)
		e.logger.Warn("command blocked by policy",
			zap.String("request_id", cmd.RequestID),
			zap.String("required_level", string(cmd.Level)),
			zap.String("probe_level", string(e.policy.Level)),
		)
		return result
	}

	// Policy check: blocked commands
	fullCmd := cmd.Command
	if len(cmd.Args) > 0 {
		fullCmd = cmd.Command + " " + strings.Join(cmd.Args, " ")
	}

	if e.isBlocked(fullCmd) {
		result.ExitCode = -1
		result.Stderr = "policy violation: command is blocked"
		e.logger.Warn("command blocked",
			zap.String("request_id", cmd.RequestID),
			zap.String("command", fullCmd),
		)
		return result
	}

	// Policy check: allowlist (if set)
	if len(e.policy.Allowed) > 0 && !e.isAllowed(fullCmd) {
		result.ExitCode = -1
		result.Stderr = "policy violation: command not in allowlist"
		return result
	}

	// Set timeout
	timeout := cmd.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Execute
	start := time.Now()
	var stdout, stderr bytes.Buffer

	c := exec.CommandContext(execCtx, cmd.Command, cmd.Args...)
	c.Stdout = &stdout
	c.Stderr = &stderr

	err := c.Run()
	result.Duration = time.Since(start).Milliseconds()

	// Capture output (truncate if needed)
	result.Stdout = truncate(stdout.String(), maxOutputSize)
	result.Stderr = truncate(stderr.String(), maxOutputSize)
	result.Truncated = stdout.Len() > maxOutputSize || stderr.Len() > maxOutputSize

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
			result.Stderr = err.Error()
		}
	}

	e.logger.Info("command executed",
		zap.String("request_id", cmd.RequestID),
		zap.String("command", cmd.Command),
		zap.Int("exit_code", result.ExitCode),
		zap.Int64("duration_ms", result.Duration),
	)

	return result
}

func (e *Executor) levelAllowed(required protocol.CapabilityLevel) bool {
	levels := map[protocol.CapabilityLevel]int{
		protocol.CapObserve:   1,
		protocol.CapDiagnose:  2,
		protocol.CapRemediate: 3,
	}
	return levels[e.policy.Level] >= levels[required]
}

func (e *Executor) isBlocked(cmd string) bool {
	lower := strings.ToLower(cmd)
	for _, b := range e.policy.Blocked {
		if strings.HasPrefix(lower, strings.ToLower(b)) {
			return true
		}
	}
	return false
}

func (e *Executor) isAllowed(cmd string) bool {
	lower := strings.ToLower(cmd)
	for _, a := range e.policy.Allowed {
		if strings.HasPrefix(lower, strings.ToLower(a)) {
			return true
		}
	}
	return false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
