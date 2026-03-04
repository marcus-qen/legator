package mcpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/sandbox"
	"github.com/marcus-qen/legator/internal/controlplane/tokenbroker"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---- test helpers -----------------------------------------------------------

const testSandboxSessionID = "mcp-test-session-1"

// newSandboxStores opens in-memory (temp dir) sandbox stores for testing.
func newSandboxStores(t *testing.T) (*sandbox.Store, *sandbox.TaskStore, *sandbox.ArtifactStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := sandbox.NewStore(filepath.Join(dir, "sandbox.db"))
	if err != nil {
		t.Fatalf("new sandbox store: %v", err)
	}
	taskStore, err := sandbox.NewTaskStore(store.DB())
	if err != nil {
		_ = store.Close()
		t.Fatalf("new task store: %v", err)
	}
	artifactStore, err := sandbox.NewArtifactStore(store.DB())
	if err != nil {
		_ = store.Close()
		t.Fatalf("new artifact store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, taskStore, artifactStore
}

// newTestBroker creates a token broker backed by an in-memory store.
func newTestBroker(t *testing.T) *tokenbroker.Broker {
	t.Helper()
	store, err := tokenbroker.NewStore(":memory:")
	if err != nil {
		t.Fatalf("new token store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return tokenbroker.NewBroker(tokenbroker.Config{Store: store})
}

// issueSandboxToken mints a valid mcp:sandbox token for tests.
// Returns (token, sessionID) that must both be passed to tool calls.
func issueSandboxToken(t *testing.T, broker *tokenbroker.Broker, runID, probeID string) (string, string) {
	t.Helper()
	issued, err := broker.Issue(tokenbroker.IssueRequest{
		RunID:     runID,
		ProbeID:   probeID,
		Audience:  AudienceMCPSandbox,
		Scopes:    []string{ScopeMCPSandbox, AudienceMCPSandbox},
		Issuer:    "test-agent",
		SessionID: testSandboxSessionID,
		TTL:       5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("issue sandbox token: %v", err)
	}
	return issued.Token, testSandboxSessionID
}

// newTestMCPServerWithSandbox creates an MCP server wired with sandbox stores + broker.
func newTestMCPServerWithSandbox(t *testing.T) (*MCPServer, *sandbox.Store, *sandbox.TaskStore, *sandbox.ArtifactStore, *tokenbroker.Broker, *audit.Store) {
	t.Helper()
	sbxStore, taskStore, artifactStore := newSandboxStores(t)
	broker := newTestBroker(t)

	srv, _, auditStore, _ := newTestMCPServerWithOptions(t, WithSandboxTools(sbxStore, taskStore, artifactStore, broker))
	return srv, sbxStore, taskStore, artifactStore, broker, auditStore
}

// callSandboxTool calls an MCP tool and parses the JSON result.
func callSandboxTool(t *testing.T, session *mcp.ClientSession, toolName string, args map[string]any) map[string]any {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("call %s: %v", toolName, err)
	}
	if result.IsError {
		// Print the error content for debugging
		text := ""
		if len(result.Content) > 0 {
			if tc, ok := result.Content[0].(*mcp.TextContent); ok {
				text = tc.Text
			}
		}
		t.Fatalf("tool %s returned error: %s", toolName, text)
	}
	if len(result.Content) == 0 {
		t.Fatalf("tool %s returned empty content", toolName)
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("tool %s returned non-text content", toolName)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(tc.Text), &out); err != nil {
		t.Fatalf("unmarshal %s result: %v (raw: %s)", toolName, err, tc.Text)
	}
	return out
}

// callSandboxToolExpectError calls a tool expecting an error result.
func callSandboxToolExpectError(t *testing.T, session *mcp.ClientSession, toolName string, args map[string]any) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		// protocol-level error is also acceptable
		return
	}
	if !result.IsError {
		t.Fatalf("tool %s expected error but got success", toolName)
	}
}

// ---- tests ------------------------------------------------------------------

func TestSandboxToolsRegistered(t *testing.T) {
	srv, _, _, _, _, _ := newTestMCPServerWithSandbox(t)
	session := connectClient(t, srv)

	result, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}

	for _, expected := range []string{
		"sandbox_create",
		"sandbox_run",
		"sandbox_read_output",
		"sandbox_get_artifact",
		"sandbox_destroy",
	} {
		if !containsString(names, expected) {
			t.Fatalf("expected tool %q in list %v", expected, names)
		}
	}
}

func TestSandboxCreateHappyPath(t *testing.T) {
	srv, _, _, _, broker, auditStore := newTestMCPServerWithSandbox(t)
	session := connectClient(t, srv)

	token, sessionID := issueSandboxToken(t, broker, "run-1", "probe-1")

	result := callSandboxTool(t, session, "sandbox_create", map[string]any{
		"run_token":    token,
		"session_id":   sessionID,
		"workspace_id": "ws-1",
		"probe_id":     "probe-1",
		"ttl_seconds":  60,
	})

	sandboxID, ok := result["sandbox_id"].(string)
	if !ok || sandboxID == "" {
		t.Fatalf("expected sandbox_id in result, got %v", result)
	}
	if result["state"] != sandbox.StateCreated {
		t.Fatalf("expected state=%q, got %v", sandbox.StateCreated, result["state"])
	}

	// Audit event should be recorded
	events := auditStore.Query(audit.Filter{Type: audit.EventSandboxCreated, Limit: 5})
	if len(events) == 0 {
		t.Fatal("expected sandbox.created audit event")
	}
}

func TestSandboxCreateInvalidToken(t *testing.T) {
	srv, _, _, _, _, auditStore := newTestMCPServerWithSandbox(t)
	session := connectClient(t, srv)

	callSandboxToolExpectError(t, session, "sandbox_create", map[string]any{
		"run_token":    "lgrt_invalidtokenvalue",
		"session_id":   "any-session",
		"workspace_id": "ws-1",
		"probe_id":     "probe-1",
	})

	// Auth denial audit event should be recorded
	events := auditStore.Query(audit.Filter{Type: audit.EventMCPSandboxDenied, Limit: 5})
	if len(events) == 0 {
		t.Fatal("expected mcp.sandbox_denied audit event on invalid token")
	}
}

func TestSandboxCreateMissingToken(t *testing.T) {
	srv, _, _, _, _, _ := newTestMCPServerWithSandbox(t)
	session := connectClient(t, srv)

	callSandboxToolExpectError(t, session, "sandbox_create", map[string]any{
		"run_token":    "",
		"workspace_id": "ws-1",
	})
}

func TestSandboxRunHappyPath(t *testing.T) {
	srv, sbxStore, _, _, broker, auditStore := newTestMCPServerWithSandbox(t)
	session := connectClient(t, srv)

	// Pre-create a sandbox session
	sess, err := sbxStore.Create(&sandbox.SandboxSession{
		WorkspaceID:  "ws-2",
		ProbeID:      "probe-2",
		RuntimeClass: "runc",
		CreatedBy:    "test",
		TTL:          time.Hour,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	// Advance state to ready
	_, _ = sbxStore.Transition(sess.ID, sandbox.StateCreated, sandbox.StateProvisioning)
	_, _ = sbxStore.Transition(sess.ID, sandbox.StateProvisioning, sandbox.StateReady)

	token, sessionID := issueSandboxToken(t, broker, "run-2", "probe-2")

	result := callSandboxTool(t, session, "sandbox_run", map[string]any{
		"run_token":  token,
		"session_id": sessionID,
		"sandbox_id": sess.ID,
		"command":    []any{"echo", "hello"},
	})

	taskID, ok := result["task_id"].(string)
	if !ok || taskID == "" {
		t.Fatalf("expected task_id in result, got %v", result)
	}
	if result["state"] != sandbox.TaskStateQueued {
		t.Fatalf("expected state=%q, got %v", sandbox.TaskStateQueued, result["state"])
	}

	// Check audit
	events := auditStore.Query(audit.Filter{Type: audit.EventSandboxTaskRun, Limit: 5})
	if len(events) == 0 {
		t.Fatal("expected sandbox.task_run audit event")
	}
}

func TestSandboxReadOutputHappyPath(t *testing.T) {
	srv, sbxStore, taskStore, _, broker, _ := newTestMCPServerWithSandbox(t)
	session := connectClient(t, srv)

	// Pre-create sandbox + task
	sess, _ := sbxStore.Create(&sandbox.SandboxSession{
		WorkspaceID: "ws-3",
		ProbeID:     "probe-3",
		CreatedBy:   "test",
		TTL:         time.Hour,
	})
	task, err := taskStore.CreateTask(&sandbox.Task{
		SandboxID:   sess.ID,
		WorkspaceID: "ws-3",
		Kind:        sandbox.TaskKindCommand,
		Command:     []string{"echo", "hi"},
		TimeoutSecs: 60,
		State:       sandbox.TaskStateQueued,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	token, sessionID := issueSandboxToken(t, broker, "run-3", "probe-3")

	result := callSandboxTool(t, session, "sandbox_read_output", map[string]any{
		"run_token":  token,
		"session_id": sessionID,
		"task_id":    task.ID,
	})

	if result["task_id"] != task.ID {
		t.Fatalf("expected task_id=%q, got %v", task.ID, result["task_id"])
	}
	if result["state"] != sandbox.TaskStateQueued {
		t.Fatalf("expected state=%q, got %v", sandbox.TaskStateQueued, result["state"])
	}
}

func TestSandboxGetArtifactHappyPath(t *testing.T) {
	srv, sbxStore, taskStore, artifactStore, broker, _ := newTestMCPServerWithSandbox(t)
	session := connectClient(t, srv)

	// Pre-create sandbox + task + artifact
	sess, _ := sbxStore.Create(&sandbox.SandboxSession{
		WorkspaceID: "ws-4",
		ProbeID:     "probe-4",
		CreatedBy:   "test",
		TTL:         time.Hour,
	})
	task, _ := taskStore.CreateTask(&sandbox.Task{
		SandboxID:   sess.ID,
		WorkspaceID: "ws-4",
		Kind:        sandbox.TaskKindCommand,
		Command:     []string{"cat", "output.txt"},
		TimeoutSecs: 60,
		State:       sandbox.TaskStateQueued,
	})
	artifact, err := artifactStore.CreateArtifact(&sandbox.Artifact{
		TaskID:      task.ID,
		SandboxID:   sess.ID,
		WorkspaceID: "ws-4",
		Path:        "output.txt",
		Kind:        sandbox.ArtifactKindFile,
		Size:        13,
		SHA256:      "abc123",
		MimeType:    "text/plain",
		Content:     []byte("hello, world\n"),
	})
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}

	token, sessionID := issueSandboxToken(t, broker, "run-4", "probe-4")

	result := callSandboxTool(t, session, "sandbox_get_artifact", map[string]any{
		"run_token":   token,
		"session_id":  sessionID,
		"artifact_id": artifact.ID,
		"sandbox_id":  sess.ID,
	})

	if result["artifact_id"] != artifact.ID {
		t.Fatalf("expected artifact_id=%q, got %v", artifact.ID, result["artifact_id"])
	}
	if result["path"] != "output.txt" {
		t.Fatalf("expected path=output.txt, got %v", result["path"])
	}
}

func TestSandboxDestroyHappyPath(t *testing.T) {
	srv, sbxStore, _, _, broker, auditStore := newTestMCPServerWithSandbox(t)
	session := connectClient(t, srv)

	// Pre-create sandbox in ready state
	sess, _ := sbxStore.Create(&sandbox.SandboxSession{
		WorkspaceID: "ws-5",
		ProbeID:     "probe-5",
		CreatedBy:   "test",
		TTL:         time.Hour,
	})
	_, _ = sbxStore.Transition(sess.ID, sandbox.StateCreated, sandbox.StateProvisioning)
	_, _ = sbxStore.Transition(sess.ID, sandbox.StateProvisioning, sandbox.StateReady)

	token, sessionID := issueSandboxToken(t, broker, "run-5", "probe-5")

	result := callSandboxTool(t, session, "sandbox_destroy", map[string]any{
		"run_token":  token,
		"session_id": sessionID,
		"sandbox_id": sess.ID,
	})

	if result["destroyed"] != true {
		t.Fatalf("expected destroyed=true, got %v", result["destroyed"])
	}
	if result["state"] != sandbox.StateDestroyed {
		t.Fatalf("expected state=%q, got %v", sandbox.StateDestroyed, result["state"])
	}

	// Confirm in store
	updated, err := sbxStore.Get(sess.ID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if updated.State != sandbox.StateDestroyed {
		t.Fatalf("expected stored state=%q, got %q", sandbox.StateDestroyed, updated.State)
	}

	// Check audit
	events := auditStore.Query(audit.Filter{Type: audit.EventSandboxDestroyed, Limit: 5})
	if len(events) == 0 {
		t.Fatal("expected sandbox.destroyed audit event")
	}
}

func TestSandboxRunInvalidToken(t *testing.T) {
	srv, sbxStore, _, _, _, auditStore := newTestMCPServerWithSandbox(t)
	session := connectClient(t, srv)

	sess, _ := sbxStore.Create(&sandbox.SandboxSession{
		WorkspaceID: "ws-6",
		ProbeID:     "probe-6",
		CreatedBy:   "test",
		TTL:         time.Hour,
	})

	callSandboxToolExpectError(t, session, "sandbox_run", map[string]any{
		"run_token":  "lgrt_badtoken",
		"session_id": "any-session",
		"sandbox_id": sess.ID,
		"command":    []any{"ls"},
	})

	events := auditStore.Query(audit.Filter{Type: audit.EventMCPSandboxDenied, Limit: 5})
	if len(events) == 0 {
		t.Fatal("expected mcp.sandbox_denied audit event on invalid token")
	}
}

func TestSandboxDestroyAlreadyDestroyed(t *testing.T) {
	srv, sbxStore, _, _, broker, _ := newTestMCPServerWithSandbox(t)
	session := connectClient(t, srv)

	// Create and fully destroy
	sess, _ := sbxStore.Create(&sandbox.SandboxSession{
		WorkspaceID: "ws-7",
		ProbeID:     "probe-7",
		CreatedBy:   "test",
		TTL:         time.Hour,
	})
	_, _ = sbxStore.Transition(sess.ID, sandbox.StateCreated, sandbox.StateProvisioning)
	_, _ = sbxStore.Transition(sess.ID, sandbox.StateProvisioning, sandbox.StateReady)
	_, _ = sbxStore.Transition(sess.ID, sandbox.StateReady, sandbox.StateDestroyed)

	token, sessionID := issueSandboxToken(t, broker, "run-7", "probe-7")

	// Destroying again should return graceful idempotent response
	result := callSandboxTool(t, session, "sandbox_destroy", map[string]any{
		"run_token":  token,
		"session_id": sessionID,
		"sandbox_id": sess.ID,
	})

	if result["state"] != sandbox.StateDestroyed {
		t.Fatalf("expected state=destroyed, got %v", result["state"])
	}
}

func TestSandboxToolsNotRegisteredWithoutOption(t *testing.T) {
	// Without WithSandboxTools, sandbox tools must not be in the list
	srv, _, _, _ := newTestMCPServer(t)
	session := connectClient(t, srv)

	result, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	sandboxToolNames := map[string]bool{
		"sandbox_create": true, "sandbox_run": true,
		"sandbox_destroy": true, "sandbox_read_output": true,
		"sandbox_get_artifact": true,
	}
	for _, tool := range result.Tools {
		if sandboxToolNames[tool.Name] {
			t.Fatalf("sandbox tool %q should not be registered without WithSandboxTools", tool.Name)
		}
	}
}
