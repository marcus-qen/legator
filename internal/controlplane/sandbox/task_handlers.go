package sandbox

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// TaskHandler exposes REST endpoints for sandbox task management.
type TaskHandler struct {
	store     *Store
	taskStore *TaskStore
	events    EventPublisher
	audit     AuditRecorder
	logger    *zap.Logger
}

// NewTaskHandler creates a TaskHandler wired to the given stores, event
// publisher, audit recorder, and logger. Any of the optional interfaces may be nil.
func NewTaskHandler(
	store *Store,
	taskStore *TaskStore,
	events EventPublisher,
	audit AuditRecorder,
	logger *zap.Logger,
) *TaskHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TaskHandler{
		store:     store,
		taskStore: taskStore,
		events:    events,
		audit:     audit,
		logger:    logger,
	}
}

// HandleCreateTask handles POST /api/v1/sandboxes/{id}/tasks
func (h *TaskHandler) HandleCreateTask(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	if sandboxID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing sandbox id")
		return
	}

	wsID := workspaceFromRequest(r)

	// Validate sandbox exists and is in ready/running state.
	sess, err := h.store.GetForWorkspace(sandboxID, wsID)
	if err != nil {
		h.logger.Error("create task: get sandbox", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch sandbox")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	if sess.State != StateReady && sess.State != StateRunning {
		writeJSONError(w, http.StatusConflict, "sandbox_not_ready",
			fmt.Sprintf("sandbox must be in ready or running state (current: %q)", sess.State))
		return
	}

	var body struct {
		Kind        string   `json:"kind"`
		Command     []string `json:"command"`
		RepoURL     string   `json:"repo_url"`
		RepoBranch  string   `json:"repo_branch"`
		RepoCommand []string `json:"repo_command"`
		Image       string   `json:"image"`
		TimeoutSecs int      `json:"timeout_secs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	body.Kind = strings.TrimSpace(body.Kind)
	if body.Kind == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "kind is required (command or repo)")
		return
	}

	switch body.Kind {
	case TaskKindCommand:
		if len(body.Command) == 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "command is required for kind=command")
			return
		}
	case TaskKindRepo:
		if strings.TrimSpace(body.RepoURL) == "" {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "repo_url is required for kind=repo")
			return
		}
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("unknown task kind %q; must be %q or %q", body.Kind, TaskKindCommand, TaskKindRepo))
		return
	}

	timeout := body.TimeoutSecs
	if timeout <= 0 {
		timeout = DefaultTaskTimeoutSecs
	}
	if timeout > MaxTaskTimeoutSecs {
		timeout = MaxTaskTimeoutSecs
	}

	task := &Task{
		SandboxID:   sandboxID,
		WorkspaceID: sess.WorkspaceID,
		Kind:        body.Kind,
		Command:     body.Command,
		RepoURL:     strings.TrimSpace(body.RepoURL),
		RepoBranch:  strings.TrimSpace(body.RepoBranch),
		RepoCommand: body.RepoCommand,
		Image:       strings.TrimSpace(body.Image),
		TimeoutSecs: timeout,
	}

	created, err := h.taskStore.CreateTask(task)
	if err != nil {
		h.logger.Error("create task", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to create task")
		return
	}

	// Transition sandbox to running if currently ready.
	if sess.State == StateReady {
		if _, tErr := h.store.Transition(sandboxID, StateReady, StateRunning); tErr != nil {
			h.logger.Warn("transition sandbox to running after task create",
				zap.String("sandbox_id", sandboxID), zap.Error(tErr))
		}
	}

	h.emitAudit("sandbox.task.created", sess.ProbeID, "api",
		fmt.Sprintf("Task created in sandbox %s: %s", sandboxID, created.ID))
	h.emitEvent("sandbox.task.created", sess.ProbeID,
		fmt.Sprintf("Task %s created in sandbox %s", created.ID, sandboxID), created)

	writeJSON(w, http.StatusCreated, created)
}

// HandleListTasks handles GET /api/v1/sandboxes/{id}/tasks
func (h *TaskHandler) HandleListTasks(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	if sandboxID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing sandbox id")
		return
	}

	wsID := workspaceFromRequest(r)

	// Validate sandbox exists (workspace isolation).
	sess, err := h.store.GetForWorkspace(sandboxID, wsID)
	if err != nil {
		h.logger.Error("list tasks: get sandbox", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch sandbox")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	tasks, err := h.taskStore.ListTasks(TaskListFilter{
		SandboxID:   sandboxID,
		WorkspaceID: sess.WorkspaceID,
	})
	if err != nil {
		h.logger.Error("list tasks", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to list tasks")
		return
	}
	if tasks == nil {
		tasks = []*Task{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tasks": tasks,
		"total": len(tasks),
	})
}

// HandleGetTask handles GET /api/v1/sandboxes/{id}/tasks/{taskId}
func (h *TaskHandler) HandleGetTask(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	taskID := r.PathValue("taskId")
	if sandboxID == "" || taskID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing sandbox or task id")
		return
	}

	wsID := workspaceFromRequest(r)

	// Validate sandbox exists (workspace isolation).
	sess, err := h.store.GetForWorkspace(sandboxID, wsID)
	if err != nil {
		h.logger.Error("get task: get sandbox", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch sandbox")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	task, err := h.taskStore.GetTaskForWorkspace(taskID, sess.WorkspaceID)
	if err != nil {
		h.logger.Error("get task", zap.String("task_id", taskID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to get task")
		return
	}
	if task == nil || task.SandboxID != sandboxID {
		writeJSONError(w, http.StatusNotFound, "not_found", "task not found")
		return
	}

	writeJSON(w, http.StatusOK, task)
}

// HandleCancelTask handles POST /api/v1/sandboxes/{id}/tasks/{taskId}/cancel
func (h *TaskHandler) HandleCancelTask(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	taskID := r.PathValue("taskId")
	if sandboxID == "" || taskID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing sandbox or task id")
		return
	}

	wsID := workspaceFromRequest(r)

	// Validate sandbox exists (workspace isolation).
	sess, err := h.store.GetForWorkspace(sandboxID, wsID)
	if err != nil {
		h.logger.Error("cancel task: get sandbox", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch sandbox")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	task, err := h.taskStore.GetTaskForWorkspace(taskID, sess.WorkspaceID)
	if err != nil {
		h.logger.Error("cancel task: get task", zap.String("task_id", taskID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch task")
		return
	}
	if task == nil || task.SandboxID != sandboxID {
		writeJSONError(w, http.StatusNotFound, "not_found", "task not found")
		return
	}

	if task.State != TaskStateQueued && task.State != TaskStateRunning {
		writeJSONError(w, http.StatusConflict, "task_not_cancellable",
			fmt.Sprintf("task is in terminal state %q and cannot be cancelled", task.State))
		return
	}

	cancelled, err := h.taskStore.TransitionTask(taskID, task.State, TaskStateCancelled)
	if err != nil {
		h.logger.Error("cancel task: transition", zap.String("task_id", taskID), zap.Error(err))
		writeJSONError(w, http.StatusConflict, "transition_failed", err.Error())
		return
	}

	h.emitAudit("sandbox.task.cancelled", sess.ProbeID, "api",
		fmt.Sprintf("Task %s cancelled in sandbox %s", taskID, sandboxID))
	h.emitEvent("sandbox.task.cancelled", sess.ProbeID,
		fmt.Sprintf("Task %s cancelled in sandbox %s", taskID, sandboxID), cancelled)

	writeJSON(w, http.StatusOK, cancelled)
}

// ── Private helpers ───────────────────────────────────────────────────────────

func (h *TaskHandler) emitEvent(typ, probeID, summary string, detail interface{}) {
	if h.events == nil {
		return
	}
	h.events.Publish(BusEvent{
		Type:      typ,
		ProbeID:   probeID,
		Summary:   summary,
		Detail:    detail,
		Timestamp: time.Now().UTC(),
	})
}

func (h *TaskHandler) emitAudit(eventType, probeID, actor, summary string) {
	if h.audit == nil {
		return
	}
	h.audit.Emit(eventType, probeID, actor, summary)
}
