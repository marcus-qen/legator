package sandbox

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

var streamUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Probes and UI clients connect from arbitrary origins.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// StreamHandler exposes HTTP endpoints for the output streaming pipeline.
type StreamHandler struct {
	store       *Store
	streamStore *StreamStore
	hub         *StreamHub
	events      EventPublisher
	audit       AuditRecorder
	logger      *zap.Logger
}

// NewStreamHandler returns a StreamHandler wired to the given dependencies.
// Any optional interface (events, audit) may be nil.
func NewStreamHandler(
	store *Store,
	streamStore *StreamStore,
	hub *StreamHub,
	events EventPublisher,
	audit AuditRecorder,
	logger *zap.Logger,
) *StreamHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &StreamHandler{
		store:       store,
		streamStore: streamStore,
		hub:         hub,
		events:      events,
		audit:       audit,
		logger:      logger,
	}
}

// HandleIngestOutput handles POST /api/v1/sandboxes/{id}/output
//
// Accepts a JSON array of chunks from a probe, validates the sandbox is
// running, persists the batch and fans out to WebSocket subscribers.
// Response: {"next_sequence": N}
func (h *StreamHandler) HandleIngestOutput(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	if sandboxID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing sandbox id")
		return
	}

	wsID := workspaceFromRequest(r)
	sess, err := h.store.GetForWorkspace(sandboxID, wsID)
	if err != nil {
		h.logger.Error("ingest output: get sandbox", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch sandbox")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	if sess.State != StateRunning {
		writeJSONError(w, http.StatusConflict, "sandbox_not_running",
			fmt.Sprintf("sandbox must be in running state (current: %q)", sess.State))
		return
	}

	var incoming []struct {
		TaskID    string    `json:"task_id"`
		Sequence  int64     `json:"sequence"`
		Stream    string    `json:"stream"`
		Data      string    `json:"data"`
		Timestamp time.Time `json:"timestamp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if len(incoming) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"next_sequence": 0})
		return
	}

	chunks := make([]*OutputChunk, 0, len(incoming))
	var maxSeq int64
	for _, ic := range incoming {
		stream := strings.TrimSpace(ic.Stream)
		if stream != StreamStdout && stream != StreamStderr {
			stream = StreamStdout
		}
		data := ic.Data
		if len(data) > MaxChunkSize {
			data = data[:MaxChunkSize]
		}
		ts := ic.Timestamp
		if ts.IsZero() {
			ts = time.Now().UTC()
		}
		c := &OutputChunk{
			TaskID:    strings.TrimSpace(ic.TaskID),
			SandboxID: sandboxID,
			Sequence:  ic.Sequence,
			Stream:    stream,
			Data:      data,
			Timestamp: ts,
		}
		chunks = append(chunks, c)
		if c.Sequence > maxSeq {
			maxSeq = c.Sequence
		}
	}

	if err := h.streamStore.AppendChunks(chunks); err != nil {
		h.logger.Error("ingest output: persist chunks",
			zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to persist output")
		return
	}

	for _, c := range chunks {
		h.hub.Broadcast(c)
	}

	h.emitAudit("sandbox.output.ingested", sess.ProbeID, "probe",
		fmt.Sprintf("Ingested %d output chunks for sandbox %s", len(chunks), sandboxID))

	writeJSON(w, http.StatusOK, map[string]any{"next_sequence": maxSeq + 1})
}

// HandleGetOutput handles GET /api/v1/sandboxes/{id}/output
//
// Query params:
//   - task_id   — filter to a specific task (optional)
//   - since     — return chunks with sequence > since (default 0)
//   - limit     — max chunks to return (default 100, max 1000)
func (h *StreamHandler) HandleGetOutput(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	if sandboxID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing sandbox id")
		return
	}

	wsID := workspaceFromRequest(r)
	sess, err := h.store.GetForWorkspace(sandboxID, wsID)
	if err != nil {
		h.logger.Error("get output: get sandbox", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch sandbox")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	q := r.URL.Query()
	taskID := strings.TrimSpace(q.Get("task_id"))

	var since int64
	if s := q.Get("since"); s != "" {
		since, _ = strconv.ParseInt(s, 10, 64)
	}

	limit := 100
	if l := q.Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	var chunks []*OutputChunk
	if taskID != "" {
		chunks, err = h.streamStore.ListChunks(taskID, since, limit)
	} else {
		chunks, err = h.streamStore.ListChunksBySandbox(sandboxID, since, limit)
	}
	if err != nil {
		h.logger.Error("get output: list chunks", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to list output")
		return
	}
	if chunks == nil {
		chunks = []*OutputChunk{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"chunks": chunks,
		"total":  len(chunks),
	})
}

// HandleStreamOutput handles GET /ws/sandboxes/{id}/stream
//
// Upgrades to WebSocket, replays existing chunks from ?since=N, then
// live-streams new chunks via the hub. Sends a ping every 30 s.
// Closes when the sandbox is destroyed or the client disconnects.
func (h *StreamHandler) HandleStreamOutput(w http.ResponseWriter, r *http.Request) {
	sandboxID := r.PathValue("id")
	if sandboxID == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing sandbox id")
		return
	}

	wsID := workspaceFromRequest(r)
	sess, err := h.store.GetForWorkspace(sandboxID, wsID)
	if err != nil {
		h.logger.Error("stream output: get sandbox", zap.String("sandbox_id", sandboxID), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to fetch sandbox")
		return
	}
	if sess == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	var since int64
	if s := r.URL.Query().Get("since"); s != "" {
		since, _ = strconv.ParseInt(s, 10, 64)
	}

	conn, err := streamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("stream output: upgrade", zap.String("sandbox_id", sandboxID), zap.Error(err))
		return
	}
	defer conn.Close()

	// Subscribe before replaying so we don't miss chunks that arrive between
	// the replay query and the subscription.
	ch, unsub := h.hub.Subscribe(sandboxID)
	defer unsub()

	// Replay historical chunks.
	historical, err := h.streamStore.ListChunksBySandbox(sandboxID, since, 1000)
	if err != nil {
		h.logger.Warn("stream output: replay query failed",
			zap.String("sandbox_id", sandboxID), zap.Error(err))
		// Non-fatal: continue with live stream only.
	}
	for _, c := range historical {
		if err := writeChunkWS(conn, c); err != nil {
			return
		}
	}

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// Ensure the read pump doesn't block the write loop: run it in a goroutine
	// and signal via a channel.
	clientGone := make(chan struct{})
	go func() {
		defer close(clientGone)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-clientGone:
			return

		case chunk, ok := <-ch:
			if !ok {
				// Hub evicted this subscriber (sandbox destroyed).
				_ = conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "sandbox destroyed"))
				return
			}
			if err := writeChunkWS(conn, chunk); err != nil {
				return
			}

		case <-pingTicker.C:
			if err := conn.WriteControl(
				websocket.PingMessage,
				nil,
				time.Now().Add(10*time.Second),
			); err != nil {
				return
			}
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writeChunkWS(conn *websocket.Conn, c *OutputChunk) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

func (h *StreamHandler) emitAudit(eventType, probeID, actor, summary string) {
	if h.audit == nil {
		return
	}
	h.audit.Emit(eventType, probeID, actor, summary)
}
