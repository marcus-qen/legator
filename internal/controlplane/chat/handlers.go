package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

var chatUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type chatRequest struct {
	Content string `json:"content"`
}

// HandleChatWS handles WebSocket connections from the chat UI.
// It bridges user messages to the chat session and streams responses back.
func (m *Manager) HandleChatWS(w http.ResponseWriter, r *http.Request) {
	probeID := r.URL.Query().Get("probe_id")
	if probeID == "" {
		http.Error(w, `{"error":"missing probe_id"}`, http.StatusBadRequest)
		return
	}

	conn, err := chatUpgrader.Upgrade(w, r, nil)
	if err != nil {
		m.logger.Error("upgrade failed", zap.Error(err), zap.String("probe_id", probeID))
		return
	}
	defer conn.Close()

	messages, cancel := m.Subscribe(probeID)
	defer cancel()

	_ = m.AddMessage(probeID, "system", fmt.Sprintf("Connected to chat for probe %s", probeID))

	done := make(chan struct{})
	go func() {
		for msg := range messages {
			if err := conn.WriteJSON(msg); err != nil {
				m.logger.Warn("failed to write chat message", zap.Error(err), zap.String("probe_id", probeID))
				_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "write error"))
				break
			}
		}
		close(done)
	}()

	for {
		var req chatRequest
		if err := conn.ReadJSON(&req); err != nil {
			break
		}
		content := strings.TrimSpace(req.Content)
		if content == "" {
			continue
		}

		if m.AddMessage(probeID, "user", content) == nil {
			m.logger.Warn("failed to persist user message", zap.String("probe_id", probeID))
			break
		}

		reply := m.respond(probeID, content)
		if m.AddMessage(probeID, "assistant", reply) == nil {
			m.logger.Warn("failed to persist assistant reply", zap.String("probe_id", probeID))
			break
		}
	}

	select {
	case <-done:
	default:
		_ = conn.Close()
	}
}

// HandleGetMessages returns chat history for a probe (REST fallback).
// GET /api/v1/probes/{id}/chat?limit=50
func (m *Manager) HandleGetMessages(w http.ResponseWriter, r *http.Request) {
	probeID := parseProbeID(r.URL.Path)
	if probeID == "" {
		http.Error(w, `{"error":"missing probe id"}`, http.StatusBadRequest)
		return
	}

	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	messages := m.GetMessages(probeID, limit)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(messages); err != nil {
		m.logger.Error("failed to encode chat history", zap.Error(err), zap.String("probe_id", probeID))
		http.Error(w, `{"error":"failed to encode response"}`, http.StatusInternalServerError)
	}
}

// HandleSendMessage sends a message via REST (non-WS fallback).
// POST /api/v1/probes/{id}/chat
func (m *Manager) HandleSendMessage(w http.ResponseWriter, r *http.Request) {
	probeID := parseProbeID(r.URL.Path)
	if probeID == "" {
		http.Error(w, `{"error":"missing probe id"}`, http.StatusBadRequest)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	content := strings.TrimSpace(req.Content)
	if content == "" {
		http.Error(w, `{"error":"message content required"}`, http.StatusBadRequest)
		return
	}

	if m.AddMessage(probeID, "user", content) == nil {
		http.Error(w, `{"error":"failed to persist user message"}`, http.StatusInternalServerError)
		return
	}

	assistant := m.AddMessage(probeID, "assistant", m.respond(probeID, content))
	if assistant == nil {
		http.Error(w, `{"error":"failed to generate assistant reply"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(assistant); err != nil {
		m.logger.Error("failed to encode assistant response", zap.Error(err), zap.String("probe_id", probeID))
		http.Error(w, `{"error":"failed to encode response"}`, http.StatusInternalServerError)
	}
}

func parseProbeID(path string) string {
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) >= 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "probes" && parts[4] == "chat" {
		return parts[3]
	}
	return ""
}

func chatReplyFor(content string) string {
	lower := strings.ToLower(content)
	if strings.HasPrefix(lower, "help") {
		return "I can’t run commands yet; this is a chat placeholder. Try asking for status, logs, or command guidance."
	}
	return "Assistant received: " + content
}
