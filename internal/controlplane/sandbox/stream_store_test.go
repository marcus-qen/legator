package sandbox

import (
	"strings"
	"testing"
	"time"
)

func newTestStreamStore(t *testing.T) (*Store, *StreamStore) {
	t.Helper()
	store := newTestStore(t)
	ss, err := NewStreamStore(store.DB())
	if err != nil {
		t.Fatalf("NewStreamStore: %v", err)
	}
	return store, ss
}

func makeChunk(sandboxID, taskID string, seq int64, stream, data string) *OutputChunk {
	return &OutputChunk{
		TaskID:    taskID,
		SandboxID: sandboxID,
		Sequence:  seq,
		Stream:    stream,
		Data:      data,
		Timestamp: time.Now().UTC(),
	}
}

// ── AppendChunk ───────────────────────────────────────────────────────────────

func TestStreamStore_AppendChunk(t *testing.T) {
	_, ss := newTestStreamStore(t)

	c := makeChunk("sbx-1", "task-1", 1, StreamStdout, "hello")
	if err := ss.AppendChunk(c); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}
	if c.ID == "" {
		t.Fatal("expected ID to be generated")
	}

	chunks, err := ss.ListChunks("task-1", 0, 100)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Data != "hello" {
		t.Errorf("expected data %q, got %q", "hello", chunks[0].Data)
	}
}

func TestStreamStore_AppendChunk_GeneratesTimestamp(t *testing.T) {
	_, ss := newTestStreamStore(t)

	c := &OutputChunk{
		TaskID:    "task-1",
		SandboxID: "sbx-1",
		Sequence:  1,
		Stream:    StreamStdout,
		Data:      "x",
		// Timestamp intentionally zero
	}
	if err := ss.AppendChunk(c); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}
	if c.Timestamp.IsZero() {
		t.Error("expected timestamp to be set")
	}
}

// ── AppendChunks ─────────────────────────────────────────────────────────────

func TestStreamStore_AppendChunks_Batch(t *testing.T) {
	_, ss := newTestStreamStore(t)

	var batch []*OutputChunk
	for i := int64(1); i <= 10; i++ {
		batch = append(batch, makeChunk("sbx-1", "task-1", i, StreamStdout, "line"))
	}
	if err := ss.AppendChunks(batch); err != nil {
		t.Fatalf("AppendChunks: %v", err)
	}

	chunks, err := ss.ListChunks("task-1", 0, 100)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(chunks) != 10 {
		t.Fatalf("expected 10 chunks, got %d", len(chunks))
	}
}

func TestStreamStore_AppendChunks_Empty(t *testing.T) {
	_, ss := newTestStreamStore(t)
	if err := ss.AppendChunks(nil); err != nil {
		t.Fatalf("AppendChunks(nil): %v", err)
	}
	if err := ss.AppendChunks([]*OutputChunk{}); err != nil {
		t.Fatalf("AppendChunks([]): %v", err)
	}
}

// ── ListChunks ordering ───────────────────────────────────────────────────────

func TestStreamStore_ListChunks_Ordering(t *testing.T) {
	_, ss := newTestStreamStore(t)

	// Insert out of order.
	seqs := []int64{5, 2, 8, 1, 3}
	for _, seq := range seqs {
		if err := ss.AppendChunk(makeChunk("sbx-1", "task-1", seq, StreamStdout, "x")); err != nil {
			t.Fatal(err)
		}
	}

	chunks, err := ss.ListChunks("task-1", 0, 100)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(chunks) != 5 {
		t.Fatalf("expected 5, got %d", len(chunks))
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i].Sequence <= chunks[i-1].Sequence {
			t.Errorf("not ordered at index %d: seq %d ≤ %d", i, chunks[i].Sequence, chunks[i-1].Sequence)
		}
	}
}

func TestStreamStore_ListChunks_SinceSequence(t *testing.T) {
	_, ss := newTestStreamStore(t)

	for i := int64(1); i <= 5; i++ {
		if err := ss.AppendChunk(makeChunk("sbx-1", "task-1", i, StreamStdout, "x")); err != nil {
			t.Fatal(err)
		}
	}

	chunks, err := ss.ListChunks("task-1", 3, 100)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks (seq 4,5), got %d", len(chunks))
	}
	if chunks[0].Sequence != 4 {
		t.Errorf("expected first seq 4, got %d", chunks[0].Sequence)
	}
}

func TestStreamStore_ListChunks_Limit(t *testing.T) {
	_, ss := newTestStreamStore(t)

	for i := int64(1); i <= 20; i++ {
		if err := ss.AppendChunk(makeChunk("sbx-1", "task-1", i, StreamStdout, "x")); err != nil {
			t.Fatal(err)
		}
	}

	chunks, err := ss.ListChunks("task-1", 0, 5)
	if err != nil {
		t.Fatalf("ListChunks: %v", err)
	}
	if len(chunks) != 5 {
		t.Fatalf("expected 5 chunks, got %d", len(chunks))
	}
}

// ── ListChunksBySandbox ───────────────────────────────────────────────────────

func TestStreamStore_ListChunksBySandbox_MultipleTasks(t *testing.T) {
	_, ss := newTestStreamStore(t)

	// Two tasks in same sandbox.
	if err := ss.AppendChunk(makeChunk("sbx-1", "task-1", 1, StreamStdout, "a")); err != nil {
		t.Fatal(err)
	}
	if err := ss.AppendChunk(makeChunk("sbx-1", "task-2", 2, StreamStdout, "b")); err != nil {
		t.Fatal(err)
	}
	if err := ss.AppendChunk(makeChunk("sbx-2", "task-3", 1, StreamStdout, "c")); err != nil {
		t.Fatal(err)
	}

	chunks, err := ss.ListChunksBySandbox("sbx-1", 0, 100)
	if err != nil {
		t.Fatalf("ListChunksBySandbox: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks for sbx-1, got %d", len(chunks))
	}
}

// ── PurgeChunks ───────────────────────────────────────────────────────────────

func TestStreamStore_PurgeChunks(t *testing.T) {
	_, ss := newTestStreamStore(t)

	for i := int64(1); i <= 5; i++ {
		if err := ss.AppendChunk(makeChunk("sbx-1", "task-1", i, StreamStdout, "x")); err != nil {
			t.Fatal(err)
		}
	}
	// Second sandbox — should not be purged.
	if err := ss.AppendChunk(makeChunk("sbx-2", "task-2", 1, StreamStdout, "y")); err != nil {
		t.Fatal(err)
	}

	if err := ss.PurgeChunks("sbx-1"); err != nil {
		t.Fatalf("PurgeChunks: %v", err)
	}

	chunks, _ := ss.ListChunksBySandbox("sbx-1", 0, 100)
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks after purge, got %d", len(chunks))
	}

	remaining, _ := ss.ListChunksBySandbox("sbx-2", 0, 100)
	if len(remaining) != 1 {
		t.Errorf("expected sbx-2 chunks intact, got %d", len(remaining))
	}
}

// ── NextSequence ─────────────────────────────────────────────────────────────

func TestStreamStore_NextSequence(t *testing.T) {
	_, ss := newTestStreamStore(t)

	next, err := ss.NextSequence("task-empty")
	if err != nil {
		t.Fatalf("NextSequence empty: %v", err)
	}
	if next != 1 {
		t.Errorf("expected 1 for empty task, got %d", next)
	}

	_ = ss.AppendChunk(makeChunk("sbx-1", "task-1", 5, StreamStdout, "x"))
	next, err = ss.NextSequence("task-1")
	if err != nil {
		t.Fatalf("NextSequence: %v", err)
	}
	if next != 6 {
		t.Errorf("expected 6, got %d", next)
	}
}

// ── Size limits ───────────────────────────────────────────────────────────────

func TestStreamStore_ChunkSizeConstant(t *testing.T) {
	if MaxChunkSize != 8*1024 {
		t.Errorf("MaxChunkSize should be 8192, got %d", MaxChunkSize)
	}
}

func TestStreamStore_ClampLimit(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 100},
		{-5, 100},
		{50, 50},
		{999, 999},
		{1000, 1000},
		{1001, 1000},
		{9999, 1000},
	}
	for _, tc := range cases {
		if got := clampLimit(tc.in); got != tc.want {
			t.Errorf("clampLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// ── Stream field round-trip ───────────────────────────────────────────────────

func TestStreamStore_StreamField(t *testing.T) {
	_, ss := newTestStreamStore(t)

	_ = ss.AppendChunk(makeChunk("sbx-1", "task-1", 1, StreamStdout, "out"))
	_ = ss.AppendChunk(makeChunk("sbx-1", "task-1", 2, StreamStderr, "err"))

	chunks, _ := ss.ListChunks("task-1", 0, 100)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0].Stream != StreamStdout {
		t.Errorf("chunk 1 stream: want %q got %q", StreamStdout, chunks[0].Stream)
	}
	if chunks[1].Stream != StreamStderr {
		t.Errorf("chunk 2 stream: want %q got %q", StreamStderr, chunks[1].Stream)
	}
}

// ── Data isolation between sandboxes ─────────────────────────────────────────

func TestStreamStore_DataIsolation(t *testing.T) {
	_, ss := newTestStreamStore(t)

	_ = ss.AppendChunk(makeChunk("sbx-A", "task-A", 1, StreamStdout, strings.Repeat("a", 100)))
	_ = ss.AppendChunk(makeChunk("sbx-B", "task-B", 1, StreamStdout, strings.Repeat("b", 100)))

	a, _ := ss.ListChunksBySandbox("sbx-A", 0, 100)
	b, _ := ss.ListChunksBySandbox("sbx-B", 0, 100)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("isolation broken: sbx-A=%d sbx-B=%d", len(a), len(b))
	}
}
