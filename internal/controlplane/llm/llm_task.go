package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

// CommandRequest is what the LLM asks us to execute.
type CommandRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Reason  string   `json:"reason"`
}

// TaskResult is the complete result of a task execution.
type TaskResult struct {
	Task       string     `json:"task"`
	ProbeID    string     `json:"probe_id"`
	Steps      []TaskStep `json:"steps"`
	Summary    string     `json:"summary"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt time.Time  `json:"finished_at"`
	Error      string     `json:"error,omitempty"`
}

// TaskStep records one command execution in the task.
type TaskStep struct {
	Command  string   `json:"command"`
	Args     []string `json:"args,omitempty"`
	Reason   string   `json:"reason"`
	ExitCode int      `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	Duration int64    `json:"duration_ms"`
}

// CommandDispatcher sends a command to a probe and waits for the result.
type CommandDispatcher func(probeID string, cmd *protocol.CommandPayload) (*protocol.CommandResultPayload, error)

// TaskRunner executes natural-language tasks against probes using an LLM.
type TaskRunner struct {
	provider Provider
	dispatch CommandDispatcher
	logger   *zap.Logger
	maxSteps int
}

// NewTaskRunner creates a TaskRunner.
func NewTaskRunner(provider Provider, dispatch CommandDispatcher, logger *zap.Logger) *TaskRunner {
	return &TaskRunner{
		provider: provider,
		dispatch: dispatch,
		logger:   logger,
		maxSteps: 10, // safety limit
	}
}

const systemPrompt = `You are Legator, an AI infrastructure management agent. You are connected to a remote server via a probe agent.

Your job: accomplish the user's task by running shell commands on the target server.

RULES:
1. Run one command at a time. Wait for the result before deciding the next step.
2. To run a command, respond with EXACTLY this JSON format (no markdown, no backticks):
   {"command": "the-command", "args": ["arg1", "arg2"], "reason": "why you're running this"}
3. When you have enough information to answer, or the task is complete, respond with a plain text summary (no JSON).
4. Be concise. Don't run unnecessary commands.
5. If a command fails, explain what went wrong and try an alternative approach.
6. NEVER run destructive commands (rm -rf /, mkfs, etc.) without being absolutely certain.
7. Respect the probe's policy level — you may only run commands appropriate for the current capability.

The target server's inventory will be provided as context.`

// Run executes a task against a probe.
func (tr *TaskRunner) Run(ctx context.Context, probeID, task string, inventory *protocol.InventoryPayload, policyLevel protocol.CapabilityLevel) (*TaskResult, error) {
	result := &TaskResult{
		Task:      task,
		ProbeID:   probeID,
		StartedAt: time.Now().UTC(),
		Steps:     []TaskStep{},
	}

	// Build initial context with inventory
	inventoryCtx := "Unknown server"
	if inventory != nil {
		inventoryCtx = fmt.Sprintf("Server: %s | OS: %s %s | Kernel: %s | CPUs: %d | RAM: %d MB | Policy: %s",
			inventory.Hostname, inventory.OS, inventory.Arch, inventory.Kernel,
			inventory.CPUs, inventory.MemTotal/(1024*1024), policyLevel)
	}

	messages := []Message{
		{Role: RoleSystem, Content: systemPrompt},
		{Role: RoleUser, Content: fmt.Sprintf("[Context] %s\n\n[Task] %s", inventoryCtx, task)},
	}

	for step := 0; step < tr.maxSteps; step++ {
		tr.logger.Info("task step",
			zap.String("probe", probeID),
			zap.Int("step", step+1),
			zap.Int("messages", len(messages)),
		)

		// Ask the LLM
		completion, err := tr.provider.Complete(ctx, &CompletionRequest{
			Messages:    messages,
			Temperature: 0.1,
			MaxTokens:   1024,
		})
		if err != nil {
			result.Error = fmt.Sprintf("LLM error at step %d: %v", step+1, err)
			result.FinishedAt = time.Now().UTC()
			return result, err
		}

		content := strings.TrimSpace(completion.Content)
		messages = append(messages, Message{Role: RoleAssistant, Content: content})

		// Try to parse as a command request
		var cmdReq CommandRequest
		if err := json.Unmarshal([]byte(content), &cmdReq); err != nil || cmdReq.Command == "" {
			// Not a command — this is the final summary
			result.Summary = content
			result.FinishedAt = time.Now().UTC()
			tr.logger.Info("task complete",
				zap.String("probe", probeID),
				zap.Int("steps", len(result.Steps)),
			)
			return result, nil
		}

		// It's a command request — dispatch it
		tr.logger.Info("dispatching command",
			zap.String("probe", probeID),
			zap.String("command", cmdReq.Command),
			zap.Strings("args", cmdReq.Args),
			zap.String("reason", cmdReq.Reason),
		)

		cmd := &protocol.CommandPayload{
			RequestID: fmt.Sprintf("task-%d-%d", time.Now().UnixNano()%100000, step),
			Command:   cmdReq.Command,
			Args:      cmdReq.Args,
			Level:     policyLevel,
			Timeout:   30 * time.Second,
		}

		cmdResult, err := tr.dispatch(probeID, cmd)

		stepRecord := TaskStep{
			Command: cmdReq.Command,
			Args:    cmdReq.Args,
			Reason:  cmdReq.Reason,
		}

		if err != nil {
			stepRecord.ExitCode = -1
			stepRecord.Stderr = err.Error()
			result.Steps = append(result.Steps, stepRecord)

			// Tell the LLM the command failed to dispatch
			messages = append(messages, Message{
				Role:    RoleUser,
				Content: fmt.Sprintf("[Error] Command dispatch failed: %s", err.Error()),
			})
			continue
		}

		stepRecord.ExitCode = cmdResult.ExitCode
		stepRecord.Stdout = cmdResult.Stdout
		stepRecord.Stderr = cmdResult.Stderr
		stepRecord.Duration = cmdResult.Duration
		result.Steps = append(result.Steps, stepRecord)

		// Truncate long output for the LLM context
		stdout := truncate(cmdResult.Stdout, 4000)
		stderr := truncate(cmdResult.Stderr, 1000)

		feedback := fmt.Sprintf("[Result] exit_code=%d duration=%dms\n", cmdResult.ExitCode, cmdResult.Duration)
		if stdout != "" {
			feedback += "stdout:\n" + stdout + "\n"
		}
		if stderr != "" {
			feedback += "stderr:\n" + stderr + "\n"
		}

		messages = append(messages, Message{Role: RoleUser, Content: feedback})
	}

	result.Summary = "Task reached maximum step limit without completing."
	result.Error = "max steps exceeded"
	result.FinishedAt = time.Now().UTC()
	return result, fmt.Errorf("task exceeded %d steps", tr.maxSteps)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n... [truncated]"
}
