// Package executor — WinRMAdapter bridges protocol.CommandPayload to WinRMExecutor.
// It applies the same policy enforcement as the local Executor but targets a
// remote Windows host via WinRM instead of running commands locally.
package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

// WinRMAdapter applies policy enforcement to WinRM command execution.
// It accepts the same CommandPayload that the local Executor accepts, translates
// the command into a PowerShell script, and executes it on a remote Windows host.
type WinRMAdapter struct {
	exec   *WinRMExecutor
	policy Policy
	logger *zap.Logger
}

// NewWinRMAdapter creates a WinRMAdapter wrapping a WinRMExecutor with a Policy.
func NewWinRMAdapter(exec *WinRMExecutor, policy Policy, logger *zap.Logger) *WinRMAdapter {
	return &WinRMAdapter{exec: exec, policy: policy, logger: logger}
}

// Execute dispatches a CommandPayload to the remote Windows host via WinRM.
// Policy checks mirror the local Executor: capability level, blocklist, and allowlist.
func (a *WinRMAdapter) Execute(ctx context.Context, cmd *protocol.CommandPayload) *protocol.CommandResultPayload {
	result := &protocol.CommandResultPayload{
		RequestID: cmd.RequestID,
	}

	// Policy: capability level (use the stricter of declared vs classified).
	requiredLevel := a.winrmEffectiveLevel(cmd)
	if !a.winrmLevelAllowed(requiredLevel) {
		result.ExitCode = -1
		result.Stderr = fmt.Sprintf(
			"policy violation: command classified as %s but probe is at %s level",
			requiredLevel, a.policy.Level,
		)
		a.logger.Warn("winrm command blocked by policy",
			zap.String("request_id", cmd.RequestID),
			zap.String("command", cmd.Command),
		)
		return result
	}

	// Policy: blocked commands.
	fullCmd := cmd.Command
	if len(cmd.Args) > 0 {
		fullCmd = cmd.Command + " " + strings.Join(cmd.Args, " ")
	}
	if a.winrmIsBlocked(fullCmd) {
		result.ExitCode = -1
		result.Stderr = "policy violation: command is blocked"
		a.logger.Warn("winrm command blocked",
			zap.String("request_id", cmd.RequestID),
			zap.String("command", fullCmd),
		)
		return result
	}

	// Policy: allowlist (if configured).
	if len(a.policy.Allowed) > 0 && !a.winrmIsAllowed(fullCmd) {
		result.ExitCode = -1
		result.Stderr = "policy violation: command not in allowlist"
		return result
	}

	// Translate command to a PowerShell script string.
	script, err := buildWinRMScript(cmd)
	if err != nil {
		result.ExitCode = -1
		result.Stderr = err.Error()
		return result
	}

	// Apply timeout (inherit defaultTimeout if unset).
	timeout := cmd.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, execErr := a.exec.Execute(execCtx, script)
	if execErr != nil && res == nil {
		result.ExitCode = -1
		result.Stderr = execErr.Error()
		return result
	}
	result.ExitCode = res.ExitCode
	result.Stdout = res.Stdout
	result.Stderr = res.Stderr
	result.Duration = res.Duration
	result.Truncated = res.Truncated

	a.logger.Info("winrm command executed",
		zap.String("request_id", cmd.RequestID),
		zap.String("command", cmd.Command),
		zap.Int("exit_code", result.ExitCode),
		zap.Int64("duration_ms", result.Duration),
	)
	return result
}

// buildWinRMScript translates a CommandPayload into a PowerShell script string.
// PowerShell/pwsh commands have their arguments used directly as the script body.
// All other commands are invoked via the call operator (&) with single-quoted paths.
func buildWinRMScript(cmd *protocol.CommandPayload) (string, error) {
	if strings.TrimSpace(cmd.Command) == "" {
		return "", fmt.Errorf("command is required")
	}

	command := strings.ToLower(strings.TrimSpace(cmd.Command))
	switch command {
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		// Strip a leading -Command flag if present (mirrors adapter.go behaviour).
		args := cmd.Args
		if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "-command") {
			args = args[1:]
		}
		script := strings.TrimSpace(strings.Join(args, " "))
		if script == "" {
			return "", fmt.Errorf("powershell command requires script arguments")
		}
		return script, nil
	default:
		// Wrap arbitrary executables: & 'path\to\exe' [args...]
		args := strings.Join(cmd.Args, " ")
		if args != "" {
			return fmt.Sprintf("& '%s' %s", cmd.Command, args), nil
		}
		return fmt.Sprintf("& '%s'", cmd.Command), nil
	}
}

// winrmEffectiveLevel returns the higher of the declared level and the classified level.
func (a *WinRMAdapter) winrmEffectiveLevel(cmd *protocol.CommandPayload) protocol.CapabilityLevel {
	declared := cmd.Level
	classified := ClassifyCommand(cmd.Command, cmd.Args)

	levels := map[protocol.CapabilityLevel]int{
		protocol.CapObserve:   1,
		protocol.CapDiagnose:  2,
		protocol.CapRemediate: 3,
	}
	if levels[classified] > levels[declared] {
		return classified
	}
	return declared
}

func (a *WinRMAdapter) winrmLevelAllowed(required protocol.CapabilityLevel) bool {
	levels := map[protocol.CapabilityLevel]int{
		protocol.CapObserve:   1,
		protocol.CapDiagnose:  2,
		protocol.CapRemediate: 3,
	}
	return levels[a.policy.Level] >= levels[required]
}

func (a *WinRMAdapter) winrmIsBlocked(cmd string) bool {
	lower := strings.ToLower(cmd)
	for _, b := range a.policy.Blocked {
		if strings.HasPrefix(lower, strings.ToLower(b)) {
			return true
		}
	}
	return false
}

func (a *WinRMAdapter) winrmIsAllowed(cmd string) bool {
	lower := strings.ToLower(cmd)
	for _, allowed := range a.policy.Allowed {
		if strings.HasPrefix(lower, strings.ToLower(allowed)) {
			return true
		}
	}
	return false
}
