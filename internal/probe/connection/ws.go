// Package connection manages the probe's WebSocket connection to the control plane.
package connection

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

const (
	heartbeatInterval = 30 * time.Second
	offlineThreshold  = 60 * time.Second
	maxReconnectDelay = 5 * time.Minute
	writeTimeout      = 10 * time.Second
	pongWait          = 70 * time.Second // slightly longer than heartbeat
)

// Client manages a persistent WebSocket connection to the control plane.
type Client struct {
	serverURL string
	apiKey    string
	probeID   string
	logger    *zap.Logger

	conn   *websocket.Conn
	mu     sync.Mutex
	inbox  chan protocol.Envelope
	closed chan struct{}
}

// NewClient creates a new WebSocket client.
func NewClient(serverURL, probeID, apiKey string, logger *zap.Logger) *Client {
	return &Client{
		serverURL: serverURL,
		probeID:   probeID,
		apiKey:    apiKey,
		logger:    logger,
		inbox:     make(chan protocol.Envelope, 64),
		closed:    make(chan struct{}),
	}
}

// Inbox returns the channel of inbound messages from the control plane.
func (c *Client) Inbox() <-chan protocol.Envelope {
	return c.inbox
}

// Run connects and maintains the WebSocket connection until ctx is cancelled.
// Reconnects automatically with exponential backoff.
func (c *Client) Run(ctx context.Context) error {
	delay := time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := c.connectAndServe(ctx)
		if err == nil || ctx.Err() != nil {
			return ctx.Err()
		}

		c.logger.Warn("connection lost, reconnecting",
			zap.Error(err),
			zap.Duration("backoff", delay),
		)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		// Exponential backoff with cap
		delay = delay * 2
		if delay > maxReconnectDelay {
			delay = maxReconnectDelay
		}
	}
}

func (c *Client) connectAndServe(ctx context.Context) error {
	url := fmt.Sprintf("%s/ws/probe?id=%s", c.serverURL, c.probeID)
	header := map[string][]string{
		"Authorization": {fmt.Sprintf("Bearer %s", c.apiKey)},
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, header)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	defer func() {
		conn.Close()
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
	}()

	c.logger.Info("connected to control plane", zap.String("url", url))

	// Start heartbeat
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	go c.heartbeatLoop(heartbeatCtx)

	// Read loop
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var env protocol.Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			c.logger.Warn("invalid message", zap.Error(err))
			continue
		}

		select {
		case c.inbox <- env:
		default:
			c.logger.Warn("inbox full, dropping message", zap.String("type", string(env.Type)))
		}
	}
}

func (c *Client) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.sendHeartbeat(); err != nil {
				c.logger.Warn("heartbeat failed", zap.Error(err))
				return
			}
		}
	}
}

func (c *Client) sendHeartbeat() error {
	hb := protocol.HeartbeatPayload{
		ProbeID: c.probeID,
	}
	return c.Send(protocol.MsgHeartbeat, hb)
}

// Send marshals and writes an envelope to the WebSocket.
func (c *Client) Send(msgType protocol.MessageType, payload any) error {
	env := protocol.Envelope{
		ID:        uuid.New().String(),
		Type:      msgType,
		Timestamp: time.Now().UTC(),
		Payload:   payload,
	}

	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("not connected")
	}

	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return c.conn.WriteMessage(websocket.TextMessage, data)
}
