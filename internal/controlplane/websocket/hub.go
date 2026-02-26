// Package websocket manages probe WebSocket connections on the control plane.
package websocket

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/marcus-qen/legator/internal/protocol"
	"github.com/marcus-qen/legator/internal/shared/signing"
	"go.uber.org/zap"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true }, // TODO: tighten
}

// ProbeConn represents a connected probe.
type ProbeConn struct {
	ID        string
	Conn      *websocket.Conn
	Connected time.Time
	LastSeen  time.Time
	mu        sync.Mutex
}

// Hub manages all connected probes.
type Hub struct {
	probes  map[string]*ProbeConn
	mu      sync.RWMutex
	logger  *zap.Logger
	onMsg   func(probeID string, env protocol.Envelope) // callback for incoming messages
	signer  *signing.Signer                             // nil = signing disabled
	streams *streamRegistry                             // output chunk subscribers
}

// NewHub creates a new Hub.
func NewHub(logger *zap.Logger, onMsg func(string, protocol.Envelope)) *Hub {
	return &Hub{
		probes:  make(map[string]*ProbeConn),
		logger:  logger,
		onMsg:   onMsg,
		streams: newStreamRegistry(),
	}
}

// SetSigner enables command signing on outgoing messages.
func (h *Hub) SetSigner(s *signing.Signer) {
	h.signer = s
}

// SubscribeStream returns a subscriber for streaming output of a command.
func (h *Hub) SubscribeStream(requestID string, bufSize int) (*StreamSubscriber, func()) {
	return h.streams.Subscribe(requestID, bufSize)
}

// DispatchChunk sends an output chunk to all subscribers for that request.
func (h *Hub) DispatchChunk(chunk protocol.OutputChunkPayload) {
	h.streams.Dispatch(chunk)
}

// HandleProbeWS is the HTTP handler for probe WebSocket connections.
func (h *Hub) HandleProbeWS(w http.ResponseWriter, r *http.Request) {
	probeID := r.URL.Query().Get("id")
	if probeID == "" {
		http.Error(w, "missing probe id", http.StatusBadRequest)
		return
	}

	// TODO: validate Authorization header / API key

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("upgrade failed", zap.Error(err))
		return
	}

	pc := &ProbeConn{
		ID:        probeID,
		Conn:      conn,
		Connected: time.Now().UTC(),
		LastSeen:  time.Now().UTC(),
	}

	h.mu.Lock()
	// Close existing connection for this probe if any
	if existing, ok := h.probes[probeID]; ok {
		existing.Conn.Close()
	}
	h.probes[probeID] = pc
	h.mu.Unlock()

	h.logger.Info("probe connected", zap.String("probe_id", probeID))

	defer func() {
		conn.Close()
		h.mu.Lock()
		if h.probes[probeID] == pc {
			delete(h.probes, probeID)
		}
		h.mu.Unlock()
		h.logger.Info("probe disconnected", zap.String("probe_id", probeID))
	}()

	// Set up ping/pong keepalive
	conn.SetPongHandler(func(string) error {
		pc.mu.Lock()
		pc.LastSeen = time.Now().UTC()
		pc.mu.Unlock()
		return conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))

	// Server-side ping loop
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			pc.mu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			pc.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	// Read loop
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var env protocol.Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			h.logger.Warn("invalid message from probe",
				zap.String("probe_id", probeID),
				zap.Error(err),
			)
			continue
		}

		pc.mu.Lock()
		pc.LastSeen = time.Now().UTC()
		pc.mu.Unlock()

		if h.onMsg != nil {
			h.onMsg(probeID, env)
		}
	}
}

// SendTo sends a message to a specific probe.
func (h *Hub) SendTo(probeID string, msgType protocol.MessageType, payload any) error {
	h.mu.RLock()
	pc, ok := h.probes[probeID]
	h.mu.RUnlock()

	if !ok {
		return fmt.Errorf("probe %s not connected", probeID)
	}

	env := protocol.Envelope{
		ID:        uuid.New().String(),
		Type:      msgType,
		Timestamp: time.Now().UTC(),
		Payload:   payload,
	}

	if h.signer != nil && msgType == protocol.MsgCommand {
		sig, err := h.signer.Sign(env.ID, payload)
		if err != nil {
			return fmt.Errorf("sign command: %w", err)
		}
		env.Signature = sig
	}

	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.Conn.WriteMessage(websocket.TextMessage, data)
}

// Connected returns a list of connected probe IDs.
func (h *Hub) Connected() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	ids := make([]string, 0, len(h.probes))
	for id := range h.probes {
		ids = append(ids, id)
	}
	return ids
}

// ProbeInfo returns basic info about a connected probe.
type ProbeInfo struct {
	ID        string    `json:"id"`
	Connected time.Time `json:"connected"`
	LastSeen  time.Time `json:"last_seen"`
	Online    bool      `json:"online"`
}

// List returns info about all connected probes.
func (h *Hub) List() []ProbeInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()

	now := time.Now().UTC()
	result := make([]ProbeInfo, 0, len(h.probes))
	for _, pc := range h.probes {
		pc.mu.Lock()
		info := ProbeInfo{
			ID:        pc.ID,
			Connected: pc.Connected,
			LastSeen:  pc.LastSeen,
			Online:    now.Sub(pc.LastSeen) < 60*time.Second,
		}
		pc.mu.Unlock()
		result = append(result, info)
	}
	return result
}
