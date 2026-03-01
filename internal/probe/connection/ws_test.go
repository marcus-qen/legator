package connection

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestClientTypeAndDefaults(t *testing.T) {
	const (
		expectedServer = "https://control.example/ws"
		expectedProbe  = "probe-conn"
		expectedAPIKey = "api-key-abc"
	)

	c := NewClient(expectedServer, expectedProbe, expectedAPIKey, zap.NewNop())
	if c == nil {
		t.Fatal("expected non-nil client")
	}

	if c.serverURL != expectedServer {
		t.Fatalf("expected serverURL %q, got %q", expectedServer, c.serverURL)
	}
	if c.probeID != expectedProbe {
		t.Fatalf("expected probeID %q, got %q", expectedProbe, c.probeID)
	}
	if c.apiKey != expectedAPIKey {
		t.Fatalf("expected apiKey %q, got %q", expectedAPIKey, c.apiKey)
	}

	if c.Inbox() == nil {
		t.Fatal("expected non-nil inbox channel")
	}
	if cap(c.inbox) != 64 {
		t.Fatalf("expected inbox capacity %d, got %d", 64, cap(c.inbox))
	}

	if err := c.Send(protocol.MsgPing, map[string]string{"x": "y"}); err == nil {
		t.Fatal("expected Send to return error when not connected")
	}
}

func TestClientTimingConstants(t *testing.T) {
	if heartbeatInterval != 30*time.Second {
		t.Fatalf("expected heartbeatInterval %s, got %s", 30*time.Second, heartbeatInterval)
	}
	if offlineThreshold != 60*time.Second {
		t.Fatalf("expected offlineThreshold %s, got %s", 60*time.Second, offlineThreshold)
	}
	if maxReconnectDelay != 5*time.Minute {
		t.Fatalf("expected maxReconnectDelay %s, got %s", 5*time.Minute, maxReconnectDelay)
	}
	if authReconnectDelay != 30*time.Second {
		t.Fatalf("expected authReconnectDelay %s, got %s", 30*time.Second, authReconnectDelay)
	}
	if writeTimeout != 10*time.Second {
		t.Fatalf("expected writeTimeout %s, got %s", 10*time.Second, writeTimeout)
	}
	if pongWait != 70*time.Second {
		t.Fatalf("expected pongWait %s, got %s", 70*time.Second, pongWait)
	}
}

func TestEnvelopeJSONSerialization(t *testing.T) {
	now := time.Date(2026, 2, 25, 23, 0, 0, 0, time.UTC)

	original := protocol.Envelope{
		ID:        "env-conn-123",
		Type:      protocol.MsgCommandResult,
		Timestamp: now,
		Payload: protocol.CommandResultPayload{
			RequestID: "req-456",
			ExitCode:  0,
			Stdout:    "ok",
			Stderr:    "",
			Duration:  123,
		},
		Signature: "sig-conn",
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var decoded protocol.Envelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if decoded.ID != original.ID {
		t.Fatalf("ID mismatch: got %q want %q", decoded.ID, original.ID)
	}
	if decoded.Type != original.Type {
		t.Fatalf("Type mismatch: got %q want %q", decoded.Type, original.Type)
	}
	if !decoded.Timestamp.Equal(original.Timestamp) {
		t.Fatalf("Timestamp mismatch: got %v want %v", decoded.Timestamp, original.Timestamp)
	}
	if decoded.Signature != original.Signature {
		t.Fatalf("Signature mismatch: got %q want %q", decoded.Signature, original.Signature)
	}

	decodedPayloadBytes, err := json.Marshal(decoded.Payload)
	if err != nil {
		t.Fatalf("marshal decoded payload: %v", err)
	}

	var decodedPayload protocol.CommandResultPayload
	if err := json.Unmarshal(decodedPayloadBytes, &decodedPayload); err != nil {
		t.Fatalf("unmarshal decoded payload: %v", err)
	}

	if decodedPayload.RequestID != original.Payload.(protocol.CommandResultPayload).RequestID {
		t.Fatalf("Payload.RequestID mismatch: got %q want %q", decodedPayload.RequestID, original.Payload.(protocol.CommandResultPayload).RequestID)
	}
}

func TestSetAPIKey(t *testing.T) {
	c := NewClient("https://control.example/ws", "probe-conn", "old-key", zap.NewNop())
	c.SetAPIKey("new-key")
	if c.apiKey != "new-key" {
		t.Fatalf("expected apiKey to be updated, got %q", c.apiKey)
	}
}

func TestRunResetsBackoffAfterSuccessfulConnection(t *testing.T) {
	var attempts atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/probe" {
			http.NotFound(w, r)
			return
		}

		switch attempts.Add(1) {
		case 3:
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade failed: %v", err)
				return
			}
			go func() {
				time.Sleep(100 * time.Millisecond)
				_ = conn.Close()
			}()
		default:
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}
	}))
	defer ts.Close()

	core, logs := observer.New(zap.WarnLevel)
	c := NewClient(wsURL(ts.URL), "probe-conn", "api-key", zap.New(core))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if logs.FilterMessage("connection lost, reconnecting").Len() >= 3 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}

	entries := logs.FilterMessage("connection lost, reconnecting").All()
	if len(entries) < 3 {
		t.Fatalf("expected at least 3 reconnect logs, got %d", len(entries))
	}

	backoffs := make([]time.Duration, 3)
	for i := 0; i < 3; i++ {
		v, ok := entries[i].ContextMap()["backoff"]
		if !ok {
			t.Fatalf("entry %d missing backoff field", i)
		}
		d, ok := v.(time.Duration)
		if !ok {
			t.Fatalf("entry %d backoff has unexpected type %T", i, v)
		}
		backoffs[i] = d
	}

	if backoffs[0] != time.Second {
		t.Fatalf("first backoff = %s, want %s", backoffs[0], time.Second)
	}
	if backoffs[1] != 2*time.Second {
		t.Fatalf("second backoff = %s, want %s", backoffs[1], 2*time.Second)
	}
	if backoffs[2] != time.Second {
		t.Fatalf("third backoff = %s, want %s (backoff should reset after successful connection)", backoffs[2], time.Second)
	}
}

func TestConnectAndServeMarksDisconnectedAfterDrop(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/probe" {
			http.NotFound(w, r)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}

		go func() {
			time.Sleep(100 * time.Millisecond)
			_ = conn.Close()
		}()
	}))
	defer ts.Close()

	c := NewClient(wsURL(ts.URL), "probe-conn", "api-key", zap.NewNop())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	wasConnected, err := c.connectAndServe(ctx)
	if err == nil {
		t.Fatal("expected connectAndServe to return error after connection drop")
	}
	if !wasConnected {
		t.Fatal("expected wasConnected=true when dial/handshake succeeded")
	}
	if c.Connected() {
		t.Fatal("expected Connected() to be false after connection drop")
	}
}

func TestConnectAndServeReturnsAuthHandshakeError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusForbidden)
	}))
	defer ts.Close()

	c := NewClient(wsURL(ts.URL), "probe-auth", "bad-key", zap.NewNop())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	wasConnected, err := c.connectAndServe(ctx)
	if err == nil {
		t.Fatal("expected auth handshake error")
	}
	if wasConnected {
		t.Fatal("auth rejection should not report wasConnected=true")
	}

	var authErr *authHandshakeError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected authHandshakeError, got %T (%v)", err, err)
	}
	if authErr.StatusCode != http.StatusForbidden {
		t.Fatalf("status code = %d, want %d", authErr.StatusCode, http.StatusForbidden)
	}
}

func TestRunUsesExtendedBackoffForAuthErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusForbidden)
	}))
	defer ts.Close()

	core, logs := observer.New(zap.WarnLevel)
	c := NewClient(wsURL(ts.URL), "probe-auth", "bad-key", zap.New(core))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- c.Run(ctx)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if logs.FilterMessage("control plane rejected probe credentials; retrying with extended backoff").Len() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	entries := logs.FilterMessage("control plane rejected probe credentials; retrying with extended backoff").All()
	if len(entries) == 0 {
		cancel()
		<-done
		t.Fatal("expected auth backoff warning log")
	}
	if got, ok := entries[0].ContextMap()["backoff"].(time.Duration); !ok || got != authReconnectDelay {
		t.Fatalf("expected backoff %s, got %#v", authReconnectDelay, entries[0].ContextMap()["backoff"])
	}
	if got, ok := entries[0].ContextMap()["status_code"].(int64); !ok || int(got) != http.StatusForbidden {
		t.Fatalf("expected status_code %d, got %#v", http.StatusForbidden, entries[0].ContextMap()["status_code"])
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}
