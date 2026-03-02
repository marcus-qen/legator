package cmdtracker

import (
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

func TestStreamRecorderReplayAndResumeSubscription(t *testing.T) {
	dbPath := t.TempDir() + "/streams.db"
	recorder, err := NewStreamRecorder(dbPath, StreamRetention{MaxEventsPerRequest: 100, MaxEventsTotal: 1000, MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	defer recorder.Close()

	if _, err := recorder.AppendMarker("req-1", StreamEventDispatch, "dispatched", map[string]any{"lane": "remediate_sandbox"}); err != nil {
		t.Fatalf("append marker: %v", err)
	}
	if _, err := recorder.AppendOutputChunk(protocol.OutputChunkPayload{RequestID: "req-1", Stream: "stdout", Data: "line-1", Seq: 1}); err != nil {
		t.Fatalf("append chunk: %v", err)
	}

	replay, sub, cleanup, err := recorder.ReplayAndSubscribe("req-1", StreamReplayQuery{LastSeq: 1}, 4)
	if err != nil {
		t.Fatalf("replay+subscribe: %v", err)
	}
	defer cleanup()

	if len(replay.Events) != 1 {
		t.Fatalf("expected replayed events=1, got %d", len(replay.Events))
	}
	if replay.Events[0].Seq != 2 {
		t.Fatalf("expected replay seq=2, got %d", replay.Events[0].Seq)
	}

	if _, err := recorder.AppendOutputChunk(protocol.OutputChunkPayload{RequestID: "req-1", Stream: "stdout", Data: "line-2", Seq: 2}); err != nil {
		t.Fatalf("append chunk 2: %v", err)
	}

	select {
	case evt := <-sub.Ch:
		if evt.Seq != 3 {
			t.Fatalf("expected live seq=3, got %d", evt.Seq)
		}
		if evt.Data != "line-2" {
			t.Fatalf("expected live data line-2, got %q", evt.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for live resumed event")
	}
}

func TestStreamRecorderRestartReplayContinuity(t *testing.T) {
	dbPath := t.TempDir() + "/streams.db"

	first, err := NewStreamRecorder(dbPath, StreamRetention{MaxEventsPerRequest: 100, MaxEventsTotal: 1000, MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}

	if _, err := first.AppendMarker("req-restart", StreamEventPolicy, "policy", map[string]any{"lane": "remediate_sandbox"}); err != nil {
		t.Fatalf("append policy marker: %v", err)
	}
	if _, err := first.AppendOutputChunk(protocol.OutputChunkPayload{RequestID: "req-restart", Stream: "stdout", Data: "hello", Seq: 1}); err != nil {
		t.Fatalf("append chunk: %v", err)
	}
	first.Close()

	second, err := NewStreamRecorder(dbPath, StreamRetention{MaxEventsPerRequest: 100, MaxEventsTotal: 1000, MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("reopen recorder: %v", err)
	}
	defer second.Close()

	replay, err := second.Replay("req-restart", StreamReplayQuery{LastSeq: 0})
	if err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if len(replay.Events) != 2 {
		t.Fatalf("expected 2 replay events, got %d", len(replay.Events))
	}
	if replay.Events[0].Kind != StreamEventPolicy || replay.Events[1].Kind != StreamEventOutput {
		t.Fatalf("unexpected replay ordering/kinds: %+v", replay.Events)
	}
	if replay.NextSeq != replay.Events[len(replay.Events)-1].Seq {
		t.Fatalf("expected next seq to match last event seq, got next=%d last=%d", replay.NextSeq, replay.Events[len(replay.Events)-1].Seq)
	}
}

func TestStreamRecorderPreventsOutOfOrderOutput(t *testing.T) {
	recorder, err := NewStreamRecorder(t.TempDir()+"/streams.db", StreamRetention{MaxEventsPerRequest: 100, MaxEventsTotal: 1000, MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	defer recorder.Close()

	emitted, err := recorder.AppendOutputChunk(protocol.OutputChunkPayload{RequestID: "req-order", Stream: "stdout", Data: "second", Seq: 2})
	if err != nil {
		t.Fatalf("append seq2: %v", err)
	}
	if len(emitted) != 0 {
		t.Fatalf("expected no emission before seq1 arrives, got %d", len(emitted))
	}

	emitted, err = recorder.AppendOutputChunk(protocol.OutputChunkPayload{RequestID: "req-order", Stream: "stdout", Data: "first", Seq: 1})
	if err != nil {
		t.Fatalf("append seq1: %v", err)
	}
	if len(emitted) != 2 {
		t.Fatalf("expected flushed events=2, got %d", len(emitted))
	}
	if emitted[0].ChunkSeq != 1 || emitted[1].ChunkSeq != 2 {
		t.Fatalf("expected chunk seq order [1,2], got [%d,%d]", emitted[0].ChunkSeq, emitted[1].ChunkSeq)
	}

	replay, err := recorder.Replay("req-order", StreamReplayQuery{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replay.Events) != 2 {
		t.Fatalf("expected replay events=2, got %d", len(replay.Events))
	}
	if replay.Events[0].ChunkSeq != 1 || replay.Events[1].ChunkSeq != 2 {
		t.Fatalf("expected replay chunk order [1,2], got [%d,%d]", replay.Events[0].ChunkSeq, replay.Events[1].ChunkSeq)
	}
}

func TestStreamRecorderTruncatedRangeIndicator(t *testing.T) {
	recorder, err := NewStreamRecorder(t.TempDir()+"/streams.db", StreamRetention{MaxEventsPerRequest: 2, MaxEventsTotal: 100, MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	defer recorder.Close()

	for i := 1; i <= 3; i++ {
		if _, err := recorder.AppendOutputChunk(protocol.OutputChunkPayload{RequestID: "req-trunc", Stream: "stdout", Data: "x", Seq: i}); err != nil {
			t.Fatalf("append chunk %d: %v", i, err)
		}
	}

	replay, err := recorder.Replay("req-trunc", StreamReplayQuery{LastSeq: 0})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Truncated {
		t.Fatal("expected replay truncated=true")
	}
	if replay.MissedFromSeq != 1 || replay.MissedToSeq != 1 {
		t.Fatalf("expected missed range 1..1, got %d..%d", replay.MissedFromSeq, replay.MissedToSeq)
	}
	if len(replay.Events) != 2 {
		t.Fatalf("expected retained replay events=2, got %d", len(replay.Events))
	}
}
