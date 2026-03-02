package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/controlplane/config"
	"github.com/marcus-qen/legator/internal/protocol"
	"go.uber.org/zap"
)

type commandReplayEnvelope struct {
	RequestID string                        `json:"request_id"`
	Replay    cmdtracker.StreamReplayResult `json:"replay"`
}

func newTestServerWithDataDir(t *testing.T, dataDir string, mutate func(*config.Config)) *Server {
	t.Helper()
	t.Setenv("LEGATOR_LLM_PROVIDER", "")
	t.Setenv("LEGATOR_AUTH", "0")
	t.Setenv("LEGATOR_SIGNING_KEY", strings.Repeat("a", 64))

	cfg := config.Config{ListenAddr: ":0", DataDir: dataDir}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func replayCommandTimeline(t *testing.T, srv *Server, requestID, query string) commandReplayEnvelope {
	t.Helper()
	path := "/api/v1/commands/" + requestID + "/replay"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetPathValue("requestId", requestID)
	rr := httptest.NewRecorder()
	srv.handleCommandReplay(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", rr.Code, rr.Body.String())
	}
	var payload commandReplayEnvelope
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode replay payload: %v", err)
	}
	return payload
}

func waitForBodyContains(t *testing.T, rr *httptest.ResponseRecorder, needle string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if strings.Contains(rr.Body.String(), needle) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for SSE body containing %q, got %q", needle, rr.Body.String())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestHandleSSEStreamReconnectResumesFromLastSeq(t *testing.T) {
	srv := newTestServer(t)
	requestID := "req-sse-resume"

	ctx1, cancel1 := context.WithCancel(context.Background())
	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/commands/"+requestID+"/stream", nil).WithContext(ctx1)
	req1.SetPathValue("requestId", requestID)
	rr1 := httptest.NewRecorder()
	done1 := make(chan struct{})
	go func() {
		srv.handleSSEStream(rr1, req1)
		close(done1)
	}()

	srv.recordCommandOutputChunk(protocol.OutputChunkPayload{RequestID: requestID, Stream: "stdout", Data: "line-1", Seq: 1}, false)
	waitForBodyContains(t, rr1, "\"line-1\"")
	cancel1()
	<-done1

	srv.recordCommandOutputChunk(protocol.OutputChunkPayload{RequestID: requestID, Stream: "stdout", Data: "line-2", Seq: 2}, false)

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/commands/"+requestID+"/stream?last_seq=1", nil).WithContext(ctx2)
	req2.SetPathValue("requestId", requestID)
	rr2 := httptest.NewRecorder()
	done2 := make(chan struct{})
	go func() {
		srv.handleSSEStream(rr2, req2)
		close(done2)
	}()
	waitForBodyContains(t, rr2, "\"line-2\"")
	cancel2()
	<-done2

	if strings.Contains(rr2.Body.String(), "\"line-1\"") {
		t.Fatalf("expected resumed SSE stream to skip already-seen line-1, body=%s", rr2.Body.String())
	}
}

func TestCommandReplayReconnectResumesFromCursor(t *testing.T) {
	srv := newTestServer(t)
	requestID := "req-reconnect"

	srv.recordCommandOutputChunk(protocol.OutputChunkPayload{RequestID: requestID, Stream: "stdout", Data: "line-1", Seq: 1}, false)

	first := replayCommandTimeline(t, srv, requestID, "last_seq=0")
	if len(first.Replay.Events) != 1 {
		t.Fatalf("expected first replay event count=1, got %d", len(first.Replay.Events))
	}
	if first.Replay.Events[0].Data != "line-1" {
		t.Fatalf("expected first replay line-1, got %q", first.Replay.Events[0].Data)
	}
	if first.Replay.ResumeToken == "" {
		t.Fatal("expected non-empty resume token")
	}

	srv.recordCommandOutputChunk(protocol.OutputChunkPayload{RequestID: requestID, Stream: "stdout", Data: "line-2", Seq: 2}, false)

	second := replayCommandTimeline(t, srv, requestID, "resume_token="+first.Replay.ResumeToken)
	if len(second.Replay.Events) != 1 {
		t.Fatalf("expected resumed replay count=1, got %d", len(second.Replay.Events))
	}
	if second.Replay.Events[0].Data != "line-2" {
		t.Fatalf("expected resumed replay line-2, got %q", second.Replay.Events[0].Data)
	}
	if second.Replay.Truncated {
		t.Fatal("did not expect truncated range on reconnect replay")
	}
}

func TestCommandReplaySurvivesServerRestart(t *testing.T) {
	dataDir := t.TempDir()
	first := newTestServerWithDataDir(t, dataDir, nil)
	requestID := "req-restart"

	first.appendCommandStreamMarker(requestID, cmdtracker.StreamEventPolicy, "policy_decision", map[string]any{"lane": "remediate_sandbox"})
	first.recordCommandOutputChunk(protocol.OutputChunkPayload{RequestID: requestID, Stream: "stdout", Data: "line-1", Seq: 1}, false)
	first.Close()

	second := newTestServerWithDataDir(t, dataDir, nil)
	replay := replayCommandTimeline(t, second, requestID, "last_seq=0")
	if len(replay.Replay.Events) != 2 {
		t.Fatalf("expected replay continuity with 2 events, got %d", len(replay.Replay.Events))
	}
	if replay.Replay.Events[0].Kind != cmdtracker.StreamEventPolicy {
		t.Fatalf("expected first event kind policy, got %s", replay.Replay.Events[0].Kind)
	}
	if replay.Replay.Events[1].Kind != cmdtracker.StreamEventOutput {
		t.Fatalf("expected second event kind output, got %s", replay.Replay.Events[1].Kind)
	}
}

func TestCommandReplayPreventsOutOfOrderChunks(t *testing.T) {
	srv := newTestServer(t)
	requestID := "req-ordered"

	srv.recordCommandOutputChunk(protocol.OutputChunkPayload{RequestID: requestID, Stream: "stdout", Data: "second", Seq: 2}, false)
	srv.recordCommandOutputChunk(protocol.OutputChunkPayload{RequestID: requestID, Stream: "stdout", Data: "first", Seq: 1}, false)

	replay := replayCommandTimeline(t, srv, requestID, "last_seq=0")
	if len(replay.Replay.Events) != 2 {
		t.Fatalf("expected replay events=2, got %d", len(replay.Replay.Events))
	}
	if replay.Replay.Events[0].ChunkSeq != 1 || replay.Replay.Events[1].ChunkSeq != 2 {
		t.Fatalf("expected chunk seq [1,2], got [%d,%d]", replay.Replay.Events[0].ChunkSeq, replay.Replay.Events[1].ChunkSeq)
	}
}
