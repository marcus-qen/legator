package chat

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHandleChatWS_MalformedJSONDoesNotCloseConnection(t *testing.T) {
	m := NewManager(testLogger())
	m.SetResponder(func(probeID, userMessage string, history []Message) (string, error) {
		return "ack", nil
	})

	ts := httptest.NewServer(http.HandlerFunc(m.HandleChatWS))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/chat?probe_id=probe-1"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial chat websocket: %v", err)
	}
	defer conn.Close()
	if resp != nil {
		_ = resp.Body.Close()
	}

	// Initial system message.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var initial Message
	if err := conn.ReadJSON(&initial); err != nil {
		t.Fatalf("read initial chat message: %v", err)
	}

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"content":`)); err != nil {
		t.Fatalf("write malformed payload: %v", err)
	}

	if err := conn.WriteJSON(map[string]string{"content": "hello"}); err != nil {
		t.Fatalf("write valid payload after malformed one: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		var got Message
		err := conn.ReadJSON(&got)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			t.Fatalf("read message after malformed payload: %v", err)
		}
		if got.Role == "assistant" && got.Content == "ack" {
			return
		}
	}

	t.Fatal("expected assistant response after malformed websocket payload")
}
