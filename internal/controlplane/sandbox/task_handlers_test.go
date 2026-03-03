package sandbox

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

// newTestTaskHandler creates a Handler + TaskHandler sharing the same SQLite db.
func newTestTaskHandler(t *testing.T) (*Handler, *TaskHandler) {
	t.Helper()
	store := newTestStore(t)
	taskStore, err := NewTaskStore(store.DB())
	if err != nil {
		t.Fatalf("NewTaskStore: %v", err)
	}
	h := NewHandler(store, &noopPublisher{}, &noopAudit{}, zap.NewNop())
	th := NewTaskHandler(store, taskStore, &noopPublisher{}, &noopAudit{}, zap.NewNop())
	return h, th
}

// mustCreateSandboxInState creates a sandbox and advances it to the given state.
func mustCreateSandboxInState(t *testing.T, mux *http.ServeMux, workspaceID, targetState string) SandboxSession {
	t.Helper()

	body := mustJSON(t, map[string]any{
		"workspace_id":  workspaceID,
		"runtime_class": "kata",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create sandbox: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var sess SandboxSession
	decodeJSON(t, w.Body, &sess)

	transitions := map[string][]struct{ from, to string }{
		StateCreated:      {},
		StateProvisioning: {{StateCreated, StateProvisioning}},
		StateReady:        {{StateCreated, StateProvisioning}, {StateProvisioning, StateReady}},
		StateRunning:      {{StateCreated, StateProvisioning}, {StateProvisioning, StateReady}, {StateReady, StateRunning}},
	}

	for _, step := range transitions[targetState] {
		tb := mustJSON(t, map[string]any{"from": step.from, "to": step.to})
		tr := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/transition", tb)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, tr)
		if rr.Code != http.StatusOK {
			t.Fatalf("transition %s→%s: expected 200, got %d", step.from, step.to, rr.Code)
		}
	}
	// Refresh session from store.
	gReq := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sess.ID, nil)
	gW := httptest.NewRecorder()
	mux.ServeHTTP(gW, gReq)
	decodeJSON(t, gW.Body, &sess)
	return sess
}

func newTaskTestMux(h *Handler, th *TaskHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)
	mux.HandleFunc("GET /api/v1/sandboxes", h.HandleList)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}", h.HandleGet)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/transition", h.HandleTransition)
	mux.HandleFunc("DELETE /api/v1/sandboxes/{id}", h.HandleDestroy)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/tasks", th.HandleCreateTask)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}/tasks", th.HandleListTasks)
	mux.HandleFunc("GET /api/v1/sandboxes/{id}/tasks/{taskId}", th.HandleGetTask)
	mux.HandleFunc("POST /api/v1/sandboxes/{id}/tasks/{taskId}/cancel", th.HandleCancelTask)
	return mux
}

// ── HandleCreateTask ──────────────────────────────────────────────────────────

func TestHandleCreateTask_CommandSuccess(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	body := mustJSON(t, map[string]any{
		"kind":    "command",
		"command": []string{"echo", "hello"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/tasks", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var task Task
	decodeJSON(t, w.Body, &task)
	if task.ID == "" {
		t.Fatal("expected task ID in response")
	}
	if task.State != TaskStateQueued {
		t.Fatalf("expected queued state, got %q", task.State)
	}
	if task.SandboxID != sess.ID {
		t.Fatalf("wrong sandbox_id: %q", task.SandboxID)
	}
}

func TestHandleCreateTask_SandboxTransitionsToRunning(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	body := mustJSON(t, map[string]any{
		"kind":    "command",
		"command": []string{"ls"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/tasks", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Verify sandbox transitioned to running.
	gReq := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sess.ID, nil)
	gW := httptest.NewRecorder()
	mux.ServeHTTP(gW, gReq)
	var updated SandboxSession
	decodeJSON(t, gW.Body, &updated)
	if updated.State != StateRunning {
		t.Fatalf("expected sandbox to be running after task create, got %q", updated.State)
	}
}

func TestHandleCreateTask_RepoSuccess(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	body := mustJSON(t, map[string]any{
		"kind":         "repo",
		"repo_url":     "https://github.com/example/repo",
		"repo_branch":  "main",
		"repo_command": []string{"make", "test"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/tasks", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var task Task
	decodeJSON(t, w.Body, &task)
	if task.Kind != TaskKindRepo {
		t.Fatalf("expected kind=repo, got %q", task.Kind)
	}
	if task.RepoURL != "https://github.com/example/repo" {
		t.Fatalf("repo_url not preserved: %q", task.RepoURL)
	}
}

func TestHandleCreateTask_SandboxNotFound(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	body := mustJSON(t, map[string]any{"kind": "command", "command": []string{"ls"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/no-such-id/tasks", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleCreateTask_SandboxNotReady(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	// Sandbox in created state (not ready or running).
	sess := mustCreateSandboxInState(t, mux, "ws-1", StateCreated)

	body := mustJSON(t, map[string]any{"kind": "command", "command": []string{"ls"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/tasks", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for non-ready sandbox, got %d: %s", w.Code, w.Body.String())
	}
	var errResp map[string]any
	json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&errResp)
	if errResp["code"] != "sandbox_not_ready" {
		t.Fatalf("expected code sandbox_not_ready, got %v", errResp["code"])
	}
}

func TestHandleCreateTask_MissingKind(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	body := mustJSON(t, map[string]any{"command": []string{"ls"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/tasks", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleCreateTask_CommandKindMissingCommand(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	body := mustJSON(t, map[string]any{"kind": "command"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/tasks", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleCreateTask_RepoKindMissingURL(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	body := mustJSON(t, map[string]any{"kind": "repo"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/tasks", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleCreateTask_UnknownKind(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	body := mustJSON(t, map[string]any{"kind": "ftp", "command": []string{"ls"}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/tasks", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown kind, got %d", w.Code)
	}
}

func TestHandleCreateTask_WorkspaceIsolation(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-owner", StateReady)

	// Different workspace cannot create a task in this sandbox.
	body := mustJSON(t, map[string]any{"kind": "command", "command": []string{"ls"}})
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sandboxes/"+sess.ID+"/tasks?workspace_id=ws-other", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-workspace task create, got %d", w.Code)
	}
}

// ── HandleListTasks ───────────────────────────────────────────────────────────

func TestHandleListTasks_Empty(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sess.ID+"/tasks", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var result map[string]any
	decodeJSON(t, w.Body, &result)
	if result["total"].(float64) != 0 {
		t.Fatalf("expected 0 total, got %v", result["total"])
	}
}

func TestHandleListTasks_ReturnsTasks(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	// Create two tasks.
	for i := 0; i < 2; i++ {
		body := mustJSON(t, map[string]any{"kind": "command", "command": []string{"ls"}})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/tasks", body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("create task %d: expected 201, got %d", i+1, w.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/"+sess.ID+"/tasks", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var result map[string]any
	decodeJSON(t, w.Body, &result)
	if result["total"].(float64) != 2 {
		t.Fatalf("expected 2 tasks, got %v", result["total"])
	}
}

func TestHandleListTasks_SandboxNotFound(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandboxes/no-such-id/tasks", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ── HandleGetTask ─────────────────────────────────────────────────────────────

func TestHandleGetTask_Success(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	body := mustJSON(t, map[string]any{"kind": "command", "command": []string{"echo", "hi"}})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/tasks", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var created Task
	decodeJSON(t, cW.Body, &created)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sandboxes/"+sess.ID+"/tasks/"+created.ID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got Task
	decodeJSON(t, w.Body, &got)
	if got.ID != created.ID {
		t.Fatalf("wrong task ID: %q != %q", got.ID, created.ID)
	}
}

func TestHandleGetTask_NotFound(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sandboxes/"+sess.ID+"/tasks/no-such-task", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleGetTask_WrongSandbox(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess1 := mustCreateSandboxInState(t, mux, "ws-1", StateReady)
	sess2 := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	body := mustJSON(t, map[string]any{"kind": "command", "command": []string{"ls"}})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess1.ID+"/tasks", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var created Task
	decodeJSON(t, cW.Body, &created)

	// Try to retrieve task from wrong sandbox.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sandboxes/"+sess2.ID+"/tasks/"+created.ID, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for task from wrong sandbox, got %d", w.Code)
	}
}

// ── HandleCancelTask ──────────────────────────────────────────────────────────

func TestHandleCancel_QueuedTask(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	body := mustJSON(t, map[string]any{"kind": "command", "command": []string{"sleep", "100"}})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/tasks", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var created Task
	decodeJSON(t, cW.Body, &created)

	// Cancel the queued task.
	cancelReq := httptest.NewRequest(http.MethodPost,
		"/api/v1/sandboxes/"+sess.ID+"/tasks/"+created.ID+"/cancel", nil)
	cancelW := httptest.NewRecorder()
	mux.ServeHTTP(cancelW, cancelReq)

	if cancelW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", cancelW.Code, cancelW.Body.String())
	}

	var cancelled Task
	decodeJSON(t, cancelW.Body, &cancelled)
	if cancelled.State != TaskStateCancelled {
		t.Fatalf("expected cancelled state, got %q", cancelled.State)
	}
}

func TestHandleCancel_TerminalTask(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	body := mustJSON(t, map[string]any{"kind": "command", "command": []string{"ls"}})
	cReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes/"+sess.ID+"/tasks", body)
	cW := httptest.NewRecorder()
	mux.ServeHTTP(cW, cReq)
	var created Task
	decodeJSON(t, cW.Body, &created)

	// Cancel once.
	r1 := httptest.NewRequest(http.MethodPost,
		"/api/v1/sandboxes/"+sess.ID+"/tasks/"+created.ID+"/cancel", nil)
	mux.ServeHTTP(httptest.NewRecorder(), r1)

	// Cancel again — should conflict.
	r2 := httptest.NewRequest(http.MethodPost,
		"/api/v1/sandboxes/"+sess.ID+"/tasks/"+created.ID+"/cancel", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, r2)

	if w2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for already-cancelled task, got %d", w2.Code)
	}
	var errResp map[string]any
	json.NewDecoder(bytes.NewReader(w2.Body.Bytes())).Decode(&errResp)
	if errResp["code"] != "task_not_cancellable" {
		t.Fatalf("expected code task_not_cancellable, got %v", errResp["code"])
	}
}

func TestHandleCancel_TaskNotFound(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	sess := mustCreateSandboxInState(t, mux, "ws-1", StateReady)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sandboxes/"+sess.ID+"/tasks/ghost-task/cancel", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleCancel_SandboxNotFound(t *testing.T) {
	h, th := newTestTaskHandler(t)
	mux := newTaskTestMux(h, th)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sandboxes/no-such-sandbox/tasks/any-task/cancel", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
