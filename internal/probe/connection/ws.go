// Package connection manages the probe's WebSocket connection to the control plane.
package connection

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

const (
	heartbeatInterval      = 30 * time.Second
	offlineThreshold       = 60 * time.Second
	maxReconnectDelay      = 5 * time.Minute
	authReconnectDelay     = 30 * time.Second
	writeTimeout           = 10 * time.Second
	pongWait               = 70 * time.Second // slightly longer than heartbeat
	authErrorBodyMaxLength = 256
)

// Client manages a persistent WebSocket connection to the control plane.
type Client struct {
	serverURL string
	apiKey    string
	probeID   string
	logger    *zap.Logger

	conn      *websocket.Conn
	mu        sync.Mutex
	connected bool
	inbox     chan protocol.Envelope
	closed    chan struct{}
}

type authHandshakeError struct {
	StatusCode int
	Body       string
}

func (e *authHandshakeError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("control plane rejected probe credentials (status=%d)", e.StatusCode)
	}
	return fmt.Sprintf("control plane rejected probe credentials (status=%d): %s", e.StatusCode, e.Body)
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

// Connected returns true if the WebSocket connection is currently established.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// SetAPIKey updates the API key used for future control-plane connections.
func (c *Client) SetAPIKey(apiKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiKey = apiKey
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

		wasConnected, err := c.connectAndServe(ctx)
		if err == nil || ctx.Err() != nil {
			return ctx.Err()
		}
		if wasConnected {
			delay = time.Second
		}

		var authErr *authHandshakeError
		if errors.As(err, &authErr) {
			if delay < authReconnectDelay {
				delay = authReconnectDelay
			}
			c.logger.Warn("control plane rejected probe credentials; retrying with extended backoff",
				zap.Int("status_code", authErr.StatusCode),
				zap.String("remediation", "re-run probe init or rotate the probe API key"),
				zap.Duration("backoff", delay),
			)
		} else {
			c.logger.Warn("connection lost, reconnecting",
				zap.Error(err),
				zap.Duration("backoff", delay),
			)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter(delay)):
		}

		if errors.As(err, &authErr) {
			// Keep retries at a steady cadence for invalid credentials so we avoid
			// hot reconnect loops while still recovering quickly after remediation.
			continue
		}

		// Exponential backoff with cap
		delay = delay * 2
		if delay > maxReconnectDelay {
			delay = maxReconnectDelay
		}
	}
}

// jitter adds 0-50% random jitter to a duration to prevent thundering herd.
func jitter(d time.Duration) time.Duration {
	max := int64(d / 2)
	if max <= 0 {
		return d
	}
	n, err := rand.Int(rand.Reader, big.NewInt(max))
	if err != nil {
		return d
	}
	return d + time.Duration(n.Int64())
}

func (c *Client) connectAndServe(ctx context.Context) (bool, error) {
	url := fmt.Sprintf("%s/ws/probe?id=%s", c.serverURL, c.probeID)
	header := map[string][]string{
		"Authorization": {fmt.Sprintf("Bearer %s", c.apiKey)},
	}

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, authErrorBodyMaxLength))
				return false, &authHandshakeError{StatusCode: resp.StatusCode, Body: string(body)}
			}
		}
		return false, fmt.Errorf("dial: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	defer func() {
		conn.Close()
		c.mu.Lock()
		c.conn = nil
		c.connected = false
		c.mu.Unlock()
	}()

	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()
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
			return true, fmt.Errorf("read: %w", err)
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
	// Send WebSocket ping frame to keep connection alive.
	// The server auto-responds with Pong, which resets our read deadline
	// via the PongHandler.
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
			return fmt.Errorf("ping: %w", err)
		}
	}

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
