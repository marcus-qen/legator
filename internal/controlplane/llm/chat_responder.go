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

const chatSystemPrompt = `You are Legator, an AI infrastructure assistant embedded in a fleet management control plane. You are chatting with an operator about a specific remote server (probe).

Your capabilities:
1. Answer questions about the server using its inventory data.
2. Run shell commands on the server to gather information or make changes.
3. Explain results clearly and suggest next steps.

IMPORTANT: Each response must be EITHER a command OR conversational text. Never mix them.

To run a command, your ENTIRE response must be this JSON and nothing else:
{"command": "the-command", "args": ["arg1", "arg2"], "reason": "why"}

To reply conversationally (no command needed), write plain text only.

Additional rules:
- Be concise and practical. You are talking to an experienced operator.
- Respect the probe policy level. At "observe" level, only read-only commands.
- At "diagnose" level, add diagnostic tools. At "remediate", you can make changes.
- NEVER run destructive commands without explicit confirmation.
- After receiving command results, summarize them clearly for the operator.
- If you need to run a command to answer a question, run it first (JSON only), then you will get the result and can explain it.`

// ChatResponder generates LLM-backed replies for probe chat sessions.
type ChatResponder struct {
	provider Provider
	dispatch CommandDispatcher
	logger   *zap.Logger
	maxSteps int
}

// ChatMessage mirrors the chat package's Message type to avoid import cycles.
type ChatMessage struct {
	Role    string
	Content string
}

// NewChatResponder creates a chat responder wired to an LLM and command dispatcher.
func NewChatResponder(provider Provider, dispatch CommandDispatcher, logger *zap.Logger) *ChatResponder {
	return &ChatResponder{
		provider: provider,
		dispatch: dispatch,
		logger:   logger,
		maxSteps: 5, // max command iterations per user message
	}
}

// Respond generates a reply to a user message in the context of a probe.
// It may execute commands on the probe and include results in the reply.
func (cr *ChatResponder) Respond(
	ctx context.Context,
	probeID string,
	history []ChatMessage,
	userMsg string,
	inventory *protocol.InventoryPayload,
	policyLevel protocol.CapabilityLevel,
) (string, error) {
	// Build inventory context
	var invCtx string
	if inventory != nil {
		invCtx = fmt.Sprintf("Probe: %s | Hostname: %s | OS: %s %s | Kernel: %s | CPUs: %d | RAM: %d MB | Policy: %s",
			probeID, inventory.Hostname, inventory.OS, inventory.Arch, inventory.Kernel,
			inventory.CPUs, inventory.MemTotal/(1024*1024), policyLevel)
		if len(inventory.Services) > 0 {
			names := make([]string, 0, min(10, len(inventory.Services)))
			for i, svc := range inventory.Services {
				if i >= 10 {
					break
				}
				names = append(names, fmt.Sprintf("%s(%s)", svc.Name, svc.State))
			}
			invCtx += "\nServices: " + strings.Join(names, ", ")
		}
	} else {
		invCtx = fmt.Sprintf("Probe: %s | Policy: %s | No inventory available yet", probeID, policyLevel)
	}

	messages := []Message{
		{Role: RoleSystem, Content: chatSystemPrompt + "\n\n[Server Context]\n" + invCtx},
	}

	// Add recent chat history (last 20 messages max to control context)
	histStart := 0
	if len(history) > 20 {
		histStart = len(history) - 20
	}
	for _, msg := range history[histStart:] {
		if msg.Role == "system" {
			continue // skip system messages from chat
		}
		messages = append(messages, Message(msg))
	}

	// Add the new user message
	messages = append(messages, Message{Role: RoleUser, Content: userMsg})

	// LLM loop: may iterate if the LLM requests commands
	for step := 0; step < cr.maxSteps; step++ {
		cr.logger.Debug("chat LLM call",
			zap.String("probe", probeID),
			zap.Int("step", step+1),
			zap.Int("messages", len(messages)),
		)

		resp, err := cr.provider.Complete(ctx, &CompletionRequest{
			Messages:    messages,
			Temperature: 0.3,
			MaxTokens:   1024,
		})
		if err != nil {
			return "", fmt.Errorf("LLM error: %w", err)
		}

		content := strings.TrimSpace(resp.Content)
		messages = append(messages, Message{Role: RoleAssistant, Content: content})

		// Try to parse as command request (exact JSON or embedded JSON)
		cmdReq, found := extractCommand(content)
		if !found {
			// Not a command — this is the conversational reply
			return content, nil
		}

		// It's a command — dispatch it
		cr.logger.Info("chat dispatching command",
			zap.String("probe", probeID),
			zap.String("command", cmdReq.Command),
			zap.Strings("args", cmdReq.Args),
			zap.String("reason", cmdReq.Reason),
		)

		cmd := &protocol.CommandPayload{
			RequestID: fmt.Sprintf("chat-%d-%d", time.Now().UnixNano()%100000, step),
			Command:   cmdReq.Command,
			Args:      cmdReq.Args,
			Level:     policyLevel,
			Timeout:   30 * time.Second,
		}

		cmdResult, err := cr.dispatch(probeID, cmd)

		if err != nil {
			messages = append(messages, Message{
				Role:    RoleUser,
				Content: fmt.Sprintf("[Command failed to dispatch: %s]", err.Error()),
			})
			continue
		}

		// Feed result back to LLM
		stdout := truncate(cmdResult.Stdout, 4000)
		stderr := truncate(cmdResult.Stderr, 1000)
		feedback := fmt.Sprintf("[Command result] exit=%d duration=%dms\n", cmdResult.ExitCode, cmdResult.Duration)
		if stdout != "" {
			feedback += "stdout:\n" + stdout + "\n"
		}
		if stderr != "" {
			feedback += "stderr:\n" + stderr + "\n"
		}
		messages = append(messages, Message{Role: RoleUser, Content: feedback})
	}

	return "I reached the maximum number of command iterations for this message. Please send another message to continue.", nil
}

// extractCommand tries to parse a command from the response.
// First tries exact JSON parse, then looks for embedded JSON object.
func extractCommand(content string) (CommandRequest, bool) {
	// Try exact parse first
	var req CommandRequest
	if err := json.Unmarshal([]byte(content), &req); err == nil && req.Command != "" {
		return req, true
	}

	// Try to find embedded JSON: look for {"command": ...}
	start := strings.Index(content, `{"command"`)
	if start == -1 {
		return CommandRequest{}, false
	}

	// Find matching closing brace
	depth := 0
	for i := start; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				candidate := content[start : i+1]
				if err := json.Unmarshal([]byte(candidate), &req); err == nil && req.Command != "" {
					return req, true
				}
				return CommandRequest{}, false
			}
		}
	}

	return CommandRequest{}, false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
