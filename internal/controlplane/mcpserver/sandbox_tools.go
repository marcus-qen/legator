package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/sandbox"
	"github.com/marcus-qen/legator/internal/controlplane/tokenbroker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ScopeMCPSandbox is the required token scope for all MCP sandbox operations.
const ScopeMCPSandbox = "mcp:sandbox"

// AudienceMCPSandbox is the expected token audience.
const AudienceMCPSandbox = "mcp"

// ---- input types ------------------------------------------------------------

type sandboxCreateInput struct {
	RunToken     string            `json:"run_token" jsonschema:"scoped run token (scope: mcp:sandbox)"`
	SessionID    string            `json:"session_id,omitempty" jsonschema:"session identifier bound to the token (for session-scoped tokens)"`
	WorkspaceID  string            `json:"workspace_id" jsonschema:"workspace identifier"`
	ProbeID      string            `json:"probe_id" jsonschema:"probe identifier"`
	RuntimeClass string            `json:"runtime_class,omitempty" jsonschema:"optional runtime class override"`
	TemplateID   string            `json:"template_id,omitempty" jsonschema:"optional template identifier"`
	TTLSeconds   int               `json:"ttl_seconds,omitempty" jsonschema:"optional TTL in seconds (default 3600)"`
	Metadata     map[string]string `json:"metadata,omitempty" jsonschema:"optional metadata key-value pairs"`
}

type sandboxRunInput struct {
	RunToken    string   `json:"run_token" jsonschema:"scoped run token (scope: mcp:sandbox)"`
	SessionID   string   `json:"session_id,omitempty" jsonschema:"session identifier bound to the token"`
	SandboxID   string   `json:"sandbox_id" jsonschema:"sandbox session identifier"`
	Command     []string `json:"command" jsonschema:"command and arguments to execute"`
	Image       string   `json:"image,omitempty" jsonschema:"optional container image override"`
	TimeoutSecs int      `json:"timeout_secs,omitempty" jsonschema:"optional timeout in seconds (default 300, max 3600)"`
}

type sandboxReadOutputInput struct {
	RunToken  string `json:"run_token" jsonschema:"scoped run token (scope: mcp:sandbox)"`
	SessionID string `json:"session_id,omitempty" jsonschema:"session identifier bound to the token"`
	TaskID    string `json:"task_id" jsonschema:"task identifier"`
}

type sandboxGetArtifactInput struct {
	RunToken   string `json:"run_token" jsonschema:"scoped run token (scope: mcp:sandbox)"`
	SessionID  string `json:"session_id,omitempty" jsonschema:"session identifier bound to the token"`
	ArtifactID string `json:"artifact_id" jsonschema:"artifact identifier"`
	SandboxID  string `json:"sandbox_id" jsonschema:"sandbox session identifier (for workspace binding)"`
}

type sandboxDestroyInput struct {
	RunToken  string `json:"run_token" jsonschema:"scoped run token (scope: mcp:sandbox)"`
	SessionID string `json:"session_id,omitempty" jsonschema:"session identifier bound to the token"`
	SandboxID string `json:"sandbox_id" jsonschema:"sandbox session identifier"`
}

// ---- handler implementations -------------------------------------------------

// validateSandboxToken validates a run token against the mcp:sandbox scope.
// sessionID may be empty; if non-empty it is matched against the token's binding.
func (s *MCPServer) validateSandboxToken(token, sessionID string) (*tokenbroker.Claims, error) {
	if s.tokenBroker == nil {
		return nil, fmt.Errorf("token broker unavailable")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("run_token is required")
	}
	claims, err := s.tokenBroker.Validate(tokenbroker.ValidateRequest{
		Token:     token,
		Scope:     ScopeMCPSandbox,
		Audience:  AudienceMCPSandbox,
		SessionID: strings.TrimSpace(sessionID),
	})
	if err != nil {
		return nil, fmt.Errorf("unauthorized: %w", err)
	}
	return claims, nil
}

// recordSandboxAudit emits an audit event for a sandbox MCP tool call.
func (s *MCPServer) recordSandboxAudit(typ audit.EventType, claims *tokenbroker.Claims, sandboxID, summary string, detail any) {
	if s.auditStore == nil {
		return
	}
	actor := claims.Issuer
	if claims.RunID != "" {
		actor = claims.Issuer + "/" + claims.RunID
	}
	s.auditStore.Record(audit.Event{
		Type:    typ,
		ProbeID: claims.ProbeID,
		Actor:   actor,
		Summary: summary,
		Detail:  detail,
	})
}

// recordSandboxDenied records an auth denial audit event.
func (s *MCPServer) recordSandboxDenied(tool string, err error) {
	if s.auditStore == nil {
		return
	}
	s.auditStore.Record(audit.Event{
		Type:    audit.EventMCPSandboxDenied,
		Summary: tool + " denied: " + err.Error(),
		Detail:  map[string]any{"tool": tool, "reason": err.Error()},
	})
}

// handleSandboxCreate creates a new sandbox session.
func (s *MCPServer) handleSandboxCreate(_ context.Context, _ *mcp.CallToolRequest, input sandboxCreateInput) (*mcp.CallToolResult, any, error) {
	claims, err := s.validateSandboxToken(input.RunToken, input.SessionID)
	if err != nil {
		s.recordSandboxDenied("sandbox_create", err)
		return nil, nil, err
	}

	if s.sandboxStore == nil {
		return nil, nil, fmt.Errorf("sandbox store unavailable")
	}

	workspaceID := strings.TrimSpace(input.WorkspaceID)
	if workspaceID == "" {
		return nil, nil, fmt.Errorf("workspace_id is required")
	}
	probeID := strings.TrimSpace(input.ProbeID)
	if probeID == "" {
		probeID = claims.ProbeID
	}

	ttl := time.Duration(input.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}

	sess := &sandbox.SandboxSession{
		WorkspaceID:  workspaceID,
		ProbeID:      probeID,
		TemplateID:   strings.TrimSpace(input.TemplateID),
		RuntimeClass: strings.TrimSpace(input.RuntimeClass),
		CreatedBy:    claims.Issuer,
		TTL:          ttl,
		Metadata:     input.Metadata,
	}

	created, err := s.sandboxStore.Create(sess)
	if err != nil {
		return nil, nil, fmt.Errorf("create sandbox: %w", err)
	}

	s.recordSandboxAudit(audit.EventSandboxCreated, claims, created.ID,
		fmt.Sprintf("sandbox created: %s (workspace=%s)", created.ID, workspaceID),
		map[string]any{
			"sandbox_id":    created.ID,
			"workspace_id":  workspaceID,
			"probe_id":      probeID,
			"run_id":        claims.RunID,
			"runtime_class": created.RuntimeClass,
		})

	return jsonToolResult(map[string]any{
		"sandbox_id":    created.ID,
		"state":         created.State,
		"workspace_id":  created.WorkspaceID,
		"probe_id":      created.ProbeID,
		"runtime_class": created.RuntimeClass,
		"template_id":   created.TemplateID,
		"created_at":    created.CreatedAt,
		"ttl_ns":        created.TTL,
	})
}

// handleSandboxRun enqueues a command task in the sandbox.
func (s *MCPServer) handleSandboxRun(_ context.Context, _ *mcp.CallToolRequest, input sandboxRunInput) (*mcp.CallToolResult, any, error) {
	claims, err := s.validateSandboxToken(input.RunToken, input.SessionID)
	if err != nil {
		s.recordSandboxDenied("sandbox_run", err)
		return nil, nil, err
	}

	if s.sandboxStore == nil || s.sandboxTaskStore == nil {
		return nil, nil, fmt.Errorf("sandbox store unavailable")
	}

	sandboxID := strings.TrimSpace(input.SandboxID)
	if sandboxID == "" {
		return nil, nil, fmt.Errorf("sandbox_id is required")
	}
	if len(input.Command) == 0 {
		return nil, nil, fmt.Errorf("command is required")
	}

	sess, err := s.sandboxStore.Get(sandboxID)
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox not found: %s", sandboxID)
	}
	if sess.IsTerminal() {
		return nil, nil, fmt.Errorf("sandbox %s is in terminal state %q", sandboxID, sess.State)
	}

	timeoutSecs := input.TimeoutSecs
	if timeoutSecs <= 0 {
		timeoutSecs = sandbox.DefaultTaskTimeoutSecs
	}
	if timeoutSecs > sandbox.MaxTaskTimeoutSecs {
		timeoutSecs = sandbox.MaxTaskTimeoutSecs
	}

	task := &sandbox.Task{
		SandboxID:   sandboxID,
		WorkspaceID: sess.WorkspaceID,
		Kind:        sandbox.TaskKindCommand,
		Command:     input.Command,
		Image:       strings.TrimSpace(input.Image),
		TimeoutSecs: timeoutSecs,
		State:       sandbox.TaskStateQueued,
	}

	created, err := s.sandboxTaskStore.CreateTask(task)
	if err != nil {
		return nil, nil, fmt.Errorf("create task: %w", err)
	}

	s.recordSandboxAudit(audit.EventSandboxTaskRun, claims, sandboxID,
		fmt.Sprintf("sandbox task queued: task=%s sandbox=%s", created.ID, sandboxID),
		map[string]any{
			"task_id":      created.ID,
			"sandbox_id":   sandboxID,
			"run_id":       claims.RunID,
			"command":      input.Command,
			"timeout_secs": timeoutSecs,
		})

	return jsonToolResult(map[string]any{
		"task_id":      created.ID,
		"sandbox_id":   sandboxID,
		"state":        created.State,
		"command":      created.Command,
		"timeout_secs": created.TimeoutSecs,
		"created_at":   created.CreatedAt,
	})
}

// handleSandboxReadOutput reads the output of a completed (or running) task.
func (s *MCPServer) handleSandboxReadOutput(_ context.Context, _ *mcp.CallToolRequest, input sandboxReadOutputInput) (*mcp.CallToolResult, any, error) {
	claims, err := s.validateSandboxToken(input.RunToken, input.SessionID)
	if err != nil {
		s.recordSandboxDenied("sandbox_read_output", err)
		return nil, nil, err
	}

	if s.sandboxTaskStore == nil {
		return nil, nil, fmt.Errorf("sandbox store unavailable")
	}

	taskID := strings.TrimSpace(input.TaskID)
	if taskID == "" {
		return nil, nil, fmt.Errorf("task_id is required")
	}

	task, err := s.sandboxTaskStore.GetTask(taskID)
	if err != nil {
		return nil, nil, fmt.Errorf("task not found: %s", taskID)
	}

	s.recordSandboxAudit(audit.EventSandboxTaskRun, claims, task.SandboxID,
		fmt.Sprintf("sandbox task output read: task=%s sandbox=%s", taskID, task.SandboxID),
		map[string]any{
			"task_id":    taskID,
			"sandbox_id": task.SandboxID,
			"run_id":     claims.RunID,
			"state":      task.State,
		})

	return jsonToolResult(map[string]any{
		"task_id":      task.ID,
		"sandbox_id":   task.SandboxID,
		"state":        task.State,
		"exit_code":    task.ExitCode,
		"output":       task.Output,
		"terminal":     task.IsTerminal(),
		"error":        task.ErrorMessage,
		"started_at":   task.StartedAt,
		"completed_at": task.CompletedAt,
	})
}

// handleSandboxGetArtifact retrieves a named artifact from a sandbox task.
func (s *MCPServer) handleSandboxGetArtifact(_ context.Context, _ *mcp.CallToolRequest, input sandboxGetArtifactInput) (*mcp.CallToolResult, any, error) {
	claims, err := s.validateSandboxToken(input.RunToken, input.SessionID)
	if err != nil {
		s.recordSandboxDenied("sandbox_get_artifact", err)
		return nil, nil, err
	}

	if s.sandboxArtifactStore == nil {
		return nil, nil, fmt.Errorf("sandbox artifact store unavailable")
	}

	artifactID := strings.TrimSpace(input.ArtifactID)
	if artifactID == "" {
		return nil, nil, fmt.Errorf("artifact_id is required")
	}
	sandboxID := strings.TrimSpace(input.SandboxID)

	// Resolve workspace_id from sandbox for binding
	workspaceID := ""
	if sandboxID != "" && s.sandboxStore != nil {
		if sess, serr := s.sandboxStore.Get(sandboxID); serr == nil {
			workspaceID = sess.WorkspaceID
		}
	}

	artifact, err := s.sandboxArtifactStore.GetArtifact(artifactID, workspaceID)
	if err != nil {
		return nil, nil, fmt.Errorf("artifact not found: %s", artifactID)
	}

	s.recordSandboxAudit(audit.EventSandboxArtifact, claims, artifact.SandboxID,
		fmt.Sprintf("sandbox artifact read: artifact=%s sandbox=%s", artifactID, artifact.SandboxID),
		map[string]any{
			"artifact_id": artifactID,
			"sandbox_id":  artifact.SandboxID,
			"task_id":     artifact.TaskID,
			"run_id":      claims.RunID,
			"path":        artifact.Path,
			"kind":        artifact.Kind,
			"size":        artifact.Size,
		})

	return jsonToolResult(map[string]any{
		"artifact_id":       artifact.ID,
		"task_id":           artifact.TaskID,
		"sandbox_id":        artifact.SandboxID,
		"path":              artifact.Path,
		"kind":              artifact.Kind,
		"size":              artifact.Size,
		"sha256":            artifact.SHA256,
		"mime_type":         artifact.MimeType,
		"diff_summary":      artifact.DiffSummary,
		"created_at":        artifact.CreatedAt,
		"content_available": len(artifact.Content) > 0,
	})
}

// handleSandboxDestroy destroys a sandbox session.
func (s *MCPServer) handleSandboxDestroy(_ context.Context, _ *mcp.CallToolRequest, input sandboxDestroyInput) (*mcp.CallToolResult, any, error) {
	claims, err := s.validateSandboxToken(input.RunToken, input.SessionID)
	if err != nil {
		s.recordSandboxDenied("sandbox_destroy", err)
		return nil, nil, err
	}

	if s.sandboxStore == nil {
		return nil, nil, fmt.Errorf("sandbox store unavailable")
	}

	sandboxID := strings.TrimSpace(input.SandboxID)
	if sandboxID == "" {
		return nil, nil, fmt.Errorf("sandbox_id is required")
	}

	sess, err := s.sandboxStore.Get(sandboxID)
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox not found: %s", sandboxID)
	}
	if sess.State == sandbox.StateDestroyed {
		return jsonToolResult(map[string]any{
			"sandbox_id": sandboxID,
			"state":      sandbox.StateDestroyed,
			"destroyed":  true,
			"message":    "sandbox already destroyed",
		})
	}

	var transErr error
	switch sess.State {
	case sandbox.StateReady, sandbox.StateRunning:
		_, transErr = s.sandboxStore.Transition(sandboxID, sess.State, sandbox.StateDestroyed)
	case sandbox.StateCreated:
		_, transErr = s.sandboxStore.Transition(sandboxID, sess.State, sandbox.StateProvisioning)
		if transErr == nil {
			_, transErr = s.sandboxStore.Transition(sandboxID, sandbox.StateProvisioning, sandbox.StateFailed)
		}
	case sandbox.StateProvisioning:
		_, transErr = s.sandboxStore.Transition(sandboxID, sess.State, sandbox.StateFailed)
	}

	if transErr != nil {
		return nil, nil, fmt.Errorf("destroy sandbox: %w", transErr)
	}

	s.recordSandboxAudit(audit.EventSandboxDestroyed, claims, sandboxID,
		fmt.Sprintf("sandbox destroyed: %s (run_id=%s)", sandboxID, claims.RunID),
		map[string]any{
			"sandbox_id":   sandboxID,
			"workspace_id": sess.WorkspaceID,
			"run_id":       claims.RunID,
			"prior_state":  sess.State,
		})

	return jsonToolResult(map[string]any{
		"sandbox_id": sandboxID,
		"state":      sandbox.StateDestroyed,
		"destroyed":  true,
	})
}
