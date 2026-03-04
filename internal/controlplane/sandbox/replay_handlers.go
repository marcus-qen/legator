package sandbox

import (
	"net/http"

	"go.uber.org/zap"
)

// ReplayHandler exposes REST endpoints for sandbox session replay.
type ReplayHandler struct {
	store         *Store
	streamStore   ChunkLister
	taskStore     TaskLister
	artifactStore ArtifactLister
	audit         AuditRecorder
	logger        *zap.Logger
}

// NewReplayHandler creates a ReplayHandler wired to the given stores,
// audit recorder, and logger.
func NewReplayHandler(
	store *Store,
	streamStore ChunkLister,
	taskStore TaskLister,
	artifactStore ArtifactLister,
	audit AuditRecorder,
	logger *zap.Logger,
) *ReplayHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ReplayHandler{
		store:         store,
		streamStore:   streamStore,
		taskStore:     taskStore,
		artifactStore: artifactStore,
		audit:         audit,
		logger:        logger,
	}
}

// HandleReplay handles GET /api/v1/sandboxes/{id}/replay
//
// Returns the full ReplayTimeline JSON. By default only terminal sandboxes
// (failed or destroyed) are served; pass ?force=1 to override.
func (h *ReplayHandler) HandleReplay(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	if sandboxID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing sandbox id")
		return
	}

	wsID := workspaceFromRequest(r)

	sess, err := h.store.GetForWorkspace(sandboxID, wsID)
	if err != nil {
		h.logger.Error("replay: get sandbox", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch sandbox")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	// Guard: only terminal sandboxes by default.
	force := r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true"
	if !force && !sess.IsTerminal() {
		writeJSONError(w, http.StatusConflict, "sandbox_not_terminal",
			"replay is only available for terminal sandboxes (failed/destroyed); use ?force=1 to override")
		return
	}

	// Emit audit event for replay access.
	if h.audit != nil {
		h.audit.Emit("sandbox.replay_accessed", sess.ProbeID, "api", "Replay timeline accessed for sandbox "+sandboxID)
	}

	timeline, err := BuildTimeline(sandboxID, wsID, h.streamStore, h.taskStore, h.artifactStore)
	if err != nil {
		h.logger.Error("replay: build timeline", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to build replay timeline")
		return
	}

	writeJSON(w, http.StatusOK, timeline)
}

// HandleReplaySummary handles GET /api/v1/sandboxes/{id}/replay/summary
//
// Returns lightweight metadata (start, end, duration, event count) without
// the events array.
func (h *ReplayHandler) HandleReplaySummary(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	if sandboxID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing sandbox id")
		return
	}

	wsID := workspaceFromRequest(r)

	sess, err := h.store.GetForWorkspace(sandboxID, wsID)
	if err != nil {
		h.logger.Error("replay summary: get sandbox", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch sandbox")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	timeline, err := BuildTimeline(sandboxID, wsID, h.streamStore, h.taskStore, h.artifactStore)
	if err != nil {
		h.logger.Error("replay summary: build timeline", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to build replay timeline")
		return
	}

	writeJSON(w, http.StatusOK, timeline.Summary())
}
