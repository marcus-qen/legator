package sandbox

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ArtifactHandler exposes REST endpoints for sandbox artifact management.
type ArtifactHandler struct {
	store         *Store
	artifactStore *ArtifactStore
	events        EventPublisher
	audit         AuditRecorder
	logger        *zap.Logger
}

// NewArtifactHandler creates an ArtifactHandler wired to the given stores,
// event publisher, audit recorder and logger. Any optional interface may be nil.
func NewArtifactHandler(
	store *Store,
	artifactStore *ArtifactStore,
	events EventPublisher,
	audit AuditRecorder,
	logger *zap.Logger,
) *ArtifactHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ArtifactHandler{
		store:         store,
		artifactStore: artifactStore,
		events:        events,
		audit:         audit,
		logger:        logger,
	}
}

// HandleUploadArtifact handles POST /api/v1/sandboxes/{id}/artifacts
//
// Multipart form fields:
//
//	file     — binary content (required)
//	path     — workspace-relative file path (required)
//	kind     — "file", "diff", or "log" (optional, defaults to "file")
//	task_id  — associated task ID (optional)
//	mime_type — MIME type (optional, auto-detected from extension if absent)
func (h *ArtifactHandler) HandleUploadArtifact(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	if sandboxID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing sandbox id")
		return
	}

	wsID := workspaceFromRequest(r)

	sess, err := h.store.GetForWorkspace(sandboxID, wsID)
	if err != nil {
		h.logger.Error("upload artifact: get sandbox", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch sandbox")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	// Limit the multipart parse to MaxArtifactSizeBytes + a small overhead for form fields.
	if err := r.ParseMultipartForm(MaxArtifactSizeBytes + 4096); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "failed to parse multipart form")
		return
	}

	// Read metadata from form fields.
	path := strings.TrimSpace(r.FormValue("path"))
	if path == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "path is required")
		return
	}

	kind := strings.TrimSpace(r.FormValue("kind"))
	if kind == "" {
		kind = ArtifactKindFile
	}
	switch kind {
	case ArtifactKindFile, ArtifactKindDiff, ArtifactKindLog:
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("unknown kind %q; must be file, diff, or log", kind))
		return
	}

	taskID := strings.TrimSpace(r.FormValue("task_id"))
	mimeType := strings.TrimSpace(r.FormValue("mime_type"))

	// Read the uploaded file content.
	f, _, fErr := r.FormFile("file")
	if fErr != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "file field is required")
		return
	}
	defer f.Close()

	content, readErr := io.ReadAll(io.LimitReader(f, MaxArtifactSizeBytes+1))
	if readErr != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to read file content")
		return
	}
	if int64(len(content)) > MaxArtifactSizeBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "artifact_too_large",
			fmt.Sprintf("artifact exceeds the %d MB per-file limit", MaxArtifactSizeBytes/(1024*1024)))
		return
	}

	if mimeType == "" {
		mimeType = detectMimeType(path, content)
	}

	artifact := &Artifact{
		TaskID:      taskID,
		SandboxID:   sandboxID,
		WorkspaceID: sess.WorkspaceID,
		Path:        path,
		Kind:        kind,
		MimeType:    mimeType,
		Content:     content,
	}

	created, err := h.artifactStore.CreateArtifact(artifact)
	if err != nil {
		if isQuotaError(err) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "sandbox_quota_exceeded", err.Error())
			return
		}
		h.logger.Error("upload artifact: create", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to create artifact")
		return
	}

	h.emitAudit("sandbox.artifact.uploaded", sess.ProbeID, "api",
		fmt.Sprintf("Artifact uploaded to sandbox %s: %s (%s)", sandboxID, path, kind))
	h.emitEvent("sandbox.artifact.uploaded", sess.ProbeID,
		fmt.Sprintf("Artifact %s uploaded to sandbox %s", created.ID, sandboxID), created)

	// Return metadata (no content).
	created.Content = nil
	writeJSON(w, http.StatusCreated, created)
}

// HandleListArtifacts handles GET /api/v1/sandboxes/{id}/artifacts
//
// Query params: task_id (optional filter)
func (h *ArtifactHandler) HandleListArtifacts(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	if sandboxID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing sandbox id")
		return
	}

	wsID := workspaceFromRequest(r)

	sess, err := h.store.GetForWorkspace(sandboxID, wsID)
	if err != nil {
		h.logger.Error("list artifacts: get sandbox", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch sandbox")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	taskID := strings.TrimSpace(r.URL.Query().Get("task_id"))

	artifacts, err := h.artifactStore.ListArtifacts(ArtifactListFilter{
		SandboxID:   sandboxID,
		WorkspaceID: sess.WorkspaceID,
		TaskID:      taskID,
	})
	if err != nil {
		h.logger.Error("list artifacts", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to list artifacts")
		return
	}
	if artifacts == nil {
		artifacts = []*Artifact{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"artifacts": artifacts,
		"total":     len(artifacts),
	})
}

// HandleGetArtifact handles GET /api/v1/sandboxes/{id}/artifacts/{artifactId}
func (h *ArtifactHandler) HandleGetArtifact(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	artifactID := r.PathValue("artifactId")
	if sandboxID == "" || artifactID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing sandbox or artifact id")
		return
	}

	wsID := workspaceFromRequest(r)

	sess, err := h.store.GetForWorkspace(sandboxID, wsID)
	if err != nil {
		h.logger.Error("get artifact: get sandbox", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch sandbox")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	artifact, err := h.artifactStore.GetArtifact(artifactID, sess.WorkspaceID)
	if err != nil {
		h.logger.Error("get artifact", zap.String("artifact_id", artifactID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to get artifact")
		return
	}
	if artifact == nil || artifact.SandboxID != sandboxID {
		writeJSONError(w, http.StatusNotFound, "not_found", "artifact not found")
		return
	}

	// Return metadata only (no content).
	artifact.Content = nil
	writeJSON(w, http.StatusOK, artifact)
}

// HandleDownloadArtifact handles GET /api/v1/sandboxes/{id}/artifacts/{artifactId}/content
func (h *ArtifactHandler) HandleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	artifactID := r.PathValue("artifactId")
	if sandboxID == "" || artifactID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing sandbox or artifact id")
		return
	}

	wsID := workspaceFromRequest(r)

	sess, err := h.store.GetForWorkspace(sandboxID, wsID)
	if err != nil {
		h.logger.Error("download artifact: get sandbox", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch sandbox")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	artifact, err := h.artifactStore.GetArtifact(artifactID, sess.WorkspaceID)
	if err != nil {
		h.logger.Error("download artifact", zap.String("artifact_id", artifactID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to get artifact")
		return
	}
	if artifact == nil || artifact.SandboxID != sandboxID {
		writeJSONError(w, http.StatusNotFound, "not_found", "artifact not found")
		return
	}

	// Determine filename for Content-Disposition.
	filename := artifact.Path
	if idx := strings.LastIndexAny(filename, "/\\"); idx >= 0 {
		filename = filename[idx+1:]
	}
	if filename == "" {
		filename = artifact.ID
	}

	mimeType := artifact.MimeType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(artifact.Content)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(artifact.Content)
}

// ── Private helpers ───────────────────────────────────────────────────────────

func (h *ArtifactHandler) emitEvent(typ, probeID, summary string, detail interface{}) {
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

func (h *ArtifactHandler) emitAudit(eventType, probeID, actor, summary string) {
	if h.audit == nil {
		return
	}
	h.audit.Emit(eventType, probeID, actor, summary)
}

// isQuotaError reports whether err is a size-limit error from ArtifactStore.
func isQuotaError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "too large") || strings.Contains(msg, "quota exceeded")
}

// detectMimeType returns a best-effort MIME type based on file extension.
func detectMimeType(path string, _ []byte) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".diff") || strings.HasSuffix(lower, ".patch"):
		return "text/x-diff"
	case strings.HasSuffix(lower, ".log"):
		return "text/plain"
	case strings.HasSuffix(lower, ".json"):
		return "application/json"
	case strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml"):
		return "application/yaml"
	case strings.HasSuffix(lower, ".txt"):
		return "text/plain"
	case strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".htm"):
		return "text/html"
	case strings.HasSuffix(lower, ".sh"):
		return "text/x-shellscript"
	case strings.HasSuffix(lower, ".go"):
		return "text/x-go"
	case strings.HasSuffix(lower, ".py"):
		return "text/x-python"
	default:
		return "application/octet-stream"
	}
}
