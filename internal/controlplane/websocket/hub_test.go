package websocket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

func waitFor(t *testing.T, timeout time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for condition after %s", timeout)
}

func probeWSURL(t *testing.T, baseURL, probeID string) string {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	u.Scheme = "ws"
	if u.Path == "" {
		u.Path = "/"
	}
	q := u.Query()
	q.Set("id", probeID)
	u.RawQuery = q.Encode()

	return u.String()
}

func dialProbeWS(t *testing.T, baseURL, probeID string) *websocket.Conn {
	t.Helper()
	wsURL := probeWSURL(t, baseURL, probeID)
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket probe connection: %v", err)
	}
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		t.Fatalf("expected switching protocols, got %d", func() int {
			if resp == nil {
				return -1
			}
			return resp.StatusCode
		}())
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	return conn
}

func containsProbe(probes []string, target string) bool {
	for _, probeID := range probes {
		if probeID == target {
			return true
		}
	}
	return false
}

func TestNewHub_InitialState(t *testing.T) {
	hub := NewHub(zap.NewNop(), nil)

	if got := hub.Connected(); len(got) != 0 {
		t.Fatalf("expected no connected probes initially, got %d", len(got))
	}

	if got := hub.List(); len(got) != 0 {
		t.Fatalf("expected empty list initially, got %d", len(got))
	}
}

func TestNewHub_SendTo_UnknownProbeReturnsError(t *testing.T) {
	hub := NewHub(zap.NewNop(), nil)

	if err := hub.SendTo("missing-probe", protocol.MsgCommand, map[string]string{"foo": "bar"}); err == nil {
		t.Fatal("expected error when sending to missing probe")
	}
}

func TestHandleProbeWS_RejectsMissingProbeID(t *testing.T) {
	hub := NewHub(zap.NewNop(), nil)

	req := httptest.NewRequest(http.MethodGet, "/ws/probe", nil)
	w := httptest.NewRecorder()
	hub.HandleProbeWS(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHandleProbeWS_ConnectAndDisconnectProbe(t *testing.T) {
	hub := NewHub(zap.NewNop(), nil)
	ts := httptest.NewServer(http.HandlerFunc(hub.HandleProbeWS))
	defer ts.Close()

	conn := dialProbeWS(t, ts.URL, "probe-one")
	defer conn.Close()

	waitFor(t, time.Second, func() bool {
		return len(hub.Connected()) == 1 && containsProbe(hub.Connected(), "probe-one")
	})

	if !containsProbe(hub.Connected(), "probe-one") {
		t.Fatalf("expected probe-one in connected list, got %#v", hub.Connected())
	}

	if err := conn.Close(); err != nil {
		t.Fatalf("close probe websocket: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		return len(hub.Connected()) == 0
	})
}

func TestHandleProbeWS_DispatchesIncomingMessages(t *testing.T) {
	msgCh := make(chan struct {
		probeID string
		msgType protocol.MessageType
	}, 1)

	hub := NewHub(zap.NewNop(), func(probeID string, env protocol.Envelope) {
		select {
		case msgCh <- struct {
			probeID string
			msgType protocol.MessageType
		}{
			probeID: probeID,
			msgType: env.Type,
		}:
		default:
		}
	})

	ts := httptest.NewServer(http.HandlerFunc(hub.HandleProbeWS))
	defer ts.Close()

	conn := dialProbeWS(t, ts.URL, "probe-emit")
	defer conn.Close()

	waitFor(t, time.Second, func() bool {
		return containsProbe(hub.Connected(), "probe-emit")
	})

	env := protocol.Envelope{
		ID:        "env-1",
		Type:      protocol.MsgHeartbeat,
		Timestamp: time.Now().UTC(),
		Payload:   protocol.HeartbeatPayload{ProbeID: "probe-emit"},
	}

	payload, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write websocket message: %v", err)
	}

	select {
	case got := <-msgCh:
		if got.probeID != "probe-emit" {
			t.Fatalf("expected callback probeID=probe-emit, got %q", got.probeID)
		}
		if got.msgType != protocol.MsgHeartbeat {
			t.Fatalf("expected callback msg type %s, got %s", protocol.MsgHeartbeat, got.msgType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for onMsg callback")
	}
}

func TestHandleProbeWS_TracksMultipleProbes(t *testing.T) {
	hub := NewHub(zap.NewNop(), nil)
	ts := httptest.NewServer(http.HandlerFunc(hub.HandleProbeWS))
	defer ts.Close()

	p1 := dialProbeWS(t, ts.URL, "probe-a")
	p2 := dialProbeWS(t, ts.URL, "probe-b")
	defer p1.Close()
	defer p2.Close()

	waitFor(t, time.Second, func() bool {
		connected := hub.Connected()
		return len(connected) == 2 && containsProbe(connected, "probe-a") && containsProbe(connected, "probe-b")
	})

	connected := hub.Connected()
	if len(connected) != 2 {
		t.Fatalf("expected 2 connected probes, got %d", len(connected))
	}
	if !containsProbe(connected, "probe-a") || !containsProbe(connected, "probe-b") {
		t.Fatalf("expected probes probe-a and probe-b, got %#v", connected)
	}
}

func TestHandleProbeWS_MalformedJSONDoesNotBreakSession(t *testing.T) {
	msgCh := make(chan struct{}, 1)

	hub := NewHub(zap.NewNop(), func(probeID string, env protocol.Envelope) {
		if probeID == "probe-malformed" && env.Type == protocol.MsgHeartbeat {
			select {
			case msgCh <- struct{}{}:
			default:
			}
		}
	})

	ts := httptest.NewServer(http.HandlerFunc(hub.HandleProbeWS))
	defer ts.Close()

	conn := dialProbeWS(t, ts.URL, "probe-malformed")
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"bad":`)); err != nil {
		t.Fatalf("write malformed probe payload: %v", err)
	}

	env := protocol.Envelope{
		ID:        "env-ok",
		Type:      protocol.MsgHeartbeat,
		Timestamp: time.Now().UTC(),
		Payload:   protocol.HeartbeatPayload{ProbeID: "probe-malformed"},
	}
	if err := conn.WriteJSON(env); err != nil {
		t.Fatalf("write valid probe payload after malformed one: %v", err)
	}

	select {
	case <-msgCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected heartbeat callback after malformed probe payload")
	}
}

func TestSendToSendsEnvelopeToConnectedProbe(t *testing.T) {
	hub := NewHub(zap.NewNop(), nil)
	ts := httptest.NewServer(http.HandlerFunc(hub.HandleProbeWS))
	defer ts.Close()

	conn := dialProbeWS(t, ts.URL, "probe-send")
	defer conn.Close()

	waitFor(t, time.Second, func() bool {
		return containsProbe(hub.Connected(), "probe-send")
	})

	payload := protocol.CommandPayload{
		RequestID: "req-123",
		Command:   "id",
		Args:      []string{"--version"},
		Timeout:   7 * time.Second,
		Level:     protocol.CapObserve,
		Stream:    true,
	}

	if err := hub.SendTo("probe-send", protocol.MsgCommand, payload); err != nil {
		t.Fatalf("send to connected probe: %v", err)
	}

	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read websocket message: %v", err)
	}

	var got protocol.Envelope
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if got.ID == "" {
		t.Fatal("expected envelope id to be populated")
	}
	if got.Type != protocol.MsgCommand {
		t.Fatalf("expected envelope type %s, got %s", protocol.MsgCommand, got.Type)
	}
	if got.Payload == nil {
		t.Fatal("expected payload in envelope")
	}
	if got.Timestamp.IsZero() {
		t.Fatal("expected timestamp in envelope")
	}

	var decoded protocol.CommandPayload
	payloadBytes, err := json.Marshal(got.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := json.Unmarshal(payloadBytes, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if decoded.RequestID != payload.RequestID {
		t.Fatalf("expected request id %q, got %q", payload.RequestID, decoded.RequestID)
	}
	if decoded.Command != payload.Command {
		t.Fatalf("expected command %q, got %q", payload.Command, decoded.Command)
	}
	if decoded.Level != payload.Level {
		t.Fatalf("expected level %q, got %q", payload.Level, decoded.Level)
	}
}

func TestHandleProbeWS_RejectsUnauthenticatedConnection(t *testing.T) {
	hub := NewHub(zap.NewNop(), nil)
	hub.SetAuthenticator(func(probeID, token string) bool {
		return probeID == "prb-good" && token == "valid-key"
	})

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleProbeWS))
	defer srv.Close()

	// No auth header → 401
	wsURL := probeWSURL(t, srv.URL, "prb-good")
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected connection to be rejected")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	// Verify no probe was registered
	if len(hub.Connected()) != 0 {
		t.Fatal("probe should not be connected")
	}
}

func TestHandleProbeWS_RejectsInvalidCredentials(t *testing.T) {
	hub := NewHub(zap.NewNop(), nil)
	hub.SetAuthenticator(func(probeID, token string) bool {
		return probeID == "prb-good" && token == "valid-key"
	})

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleProbeWS))
	defer srv.Close()

	// Wrong token → 403
	wsURL := probeWSURL(t, srv.URL, "prb-good")
	header := http.Header{"Authorization": []string{"Bearer wrong-key"}}
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		t.Fatal("expected connection to be rejected")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestHandleProbeWS_AcceptsValidCredentials(t *testing.T) {
	hub := NewHub(zap.NewNop(), nil)
	hub.SetAuthenticator(func(probeID, token string) bool {
		return probeID == "prb-authed" && token == "valid-key-123"
	})

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleProbeWS))
	defer srv.Close()

	wsURL := probeWSURL(t, srv.URL, "prb-authed")
	header := http.Header{"Authorization": []string{"Bearer valid-key-123"}}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("expected connection to succeed: %v", err)
	}
	defer conn.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101, got %d", resp.StatusCode)
	}

	waitFor(t, 500*time.Millisecond, func() bool {
		return containsProbe(hub.Connected(), "prb-authed")
	})
}

func TestHandleProbeWS_NoAuthenticatorAllowsAll(t *testing.T) {
	// nil authenticator = no auth check (backward compat / test mode)
	hub := NewHub(zap.NewNop(), nil)

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleProbeWS))
	defer srv.Close()

	conn := dialProbeWS(t, srv.URL, "prb-noauth")
	defer conn.Close()

	waitFor(t, 500*time.Millisecond, func() bool {
		return containsProbe(hub.Connected(), "prb-noauth")
	})
}
