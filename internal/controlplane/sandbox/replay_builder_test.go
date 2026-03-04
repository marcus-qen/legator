package sandbox

import (
	"testing"
	"time"
)

// ── Mock stores ──────────────────────────────────────────────────────────────

type mockChunkLister struct {
	chunks []*OutputChunk
	err    error
}

func (m *mockChunkLister) ListChunksBySandbox(sandboxID string, sinceSeq int64, limit int) ([]*OutputChunk, error) {
	if m.err != nil {
		return nil, m.err
	}
	var out []*OutputChunk
	for _, c := range m.chunks {
		if c.SandboxID == sandboxID && c.Sequence > sinceSeq {
			out = append(out, c)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type mockTaskLister struct {
	tasks []*Task
	err   error
}

func (m *mockTaskLister) ListTasks(f TaskListFilter) ([]*Task, error) {
	if m.err != nil {
		return nil, m.err
	}
	var out []*Task
	for _, t := range m.tasks {
		if f.SandboxID != "" && t.SandboxID != f.SandboxID {
			continue
		}
		if f.WorkspaceID != "" && t.WorkspaceID != "" && t.WorkspaceID != f.WorkspaceID {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

type mockArtifactLister struct {
	artifacts []*Artifact
	err       error
}

func (m *mockArtifactLister) ListArtifacts(f ArtifactListFilter) ([]*Artifact, error) {
	if m.err != nil {
		return nil, m.err
	}
	var out []*Artifact
	for _, a := range m.artifacts {
		if f.SandboxID != "" && a.SandboxID != f.SandboxID {
			continue
		}
		if f.WorkspaceID != "" && a.WorkspaceID != "" && a.WorkspaceID != f.WorkspaceID {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func mustBuildTimeline(t *testing.T, sandboxID, wsID string, cl ChunkLister, tl TaskLister, al ArtifactLister) *ReplayTimeline {
	t.Helper()
	timeline, err := BuildTimeline(sandboxID, wsID, cl, tl, al)
	if err != nil {
		t.Fatalf("BuildTimeline: %v", err)
	}
	return timeline
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestBuildTimeline_EmptySandbox(t *testing.T) {
	timeline := mustBuildTimeline(t, "sbx-1", "", &mockChunkLister{}, &mockTaskLister{}, &mockArtifactLister{})

	if timeline.SandboxID != "sbx-1" {
		t.Errorf("expected sandbox_id=sbx-1, got %q", timeline.SandboxID)
	}
	if timeline.EventCount != 0 {
		t.Errorf("expected 0 events, got %d", timeline.EventCount)
	}
	if len(timeline.Events) != 0 {
		t.Errorf("expected empty events slice, got %d entries", len(timeline.Events))
	}
	if !timeline.StartTime.IsZero() {
		t.Errorf("expected zero StartTime, got %v", timeline.StartTime)
	}
}

func TestBuildTimeline_OutputChunksOnly(t *testing.T) {
	now := time.Now().UTC()
	chunks := []*OutputChunk{
		{ID: "c1", SandboxID: "sbx-1", Sequence: 1, Stream: "stdout", Data: "hello", Timestamp: now.Add(1 * time.Second)},
		{ID: "c2", SandboxID: "sbx-1", Sequence: 2, Stream: "stdout", Data: "world", Timestamp: now.Add(2 * time.Second)},
		{ID: "c3", SandboxID: "sbx-1", Sequence: 3, Stream: "stderr", Data: "err", Timestamp: now.Add(3 * time.Second)},
	}

	cl := &mockChunkLister{chunks: chunks}
	timeline := mustBuildTimeline(t, "sbx-1", "", cl, &mockTaskLister{}, &mockArtifactLister{})

	if timeline.EventCount != 3 {
		t.Errorf("expected 3 events, got %d", timeline.EventCount)
	}
	for _, evt := range timeline.Events {
		if evt.Kind != ReplayEventKindOutput {
			t.Errorf("expected output kind, got %q", evt.Kind)
		}
	}
	// Verify strict ordering.
	for i := 1; i < len(timeline.Events); i++ {
		if timeline.Events[i].Timestamp.Before(timeline.Events[i-1].Timestamp) {
			t.Errorf("events out of order at index %d", i)
		}
	}
}

func TestBuildTimeline_TaskEvents(t *testing.T) {
	now := time.Now().UTC()
	started := now.Add(1 * time.Second)
	completed := now.Add(5 * time.Second)

	taskList := []*Task{
		{
			ID:          "task-1",
			SandboxID:   "sbx-1",
			WorkspaceID: "ws-1",
			Kind:        TaskKindCommand,
			State:       TaskStateSucceeded,
			CreatedAt:   now,
			StartedAt:   &started,
			CompletedAt: &completed,
		},
	}

	tl := &mockTaskLister{tasks: taskList}
	timeline := mustBuildTimeline(t, "sbx-1", "ws-1", &mockChunkLister{}, tl, &mockArtifactLister{})

	// Expect 3 task state events: queued, running, succeeded.
	if timeline.EventCount != 3 {
		t.Errorf("expected 3 task state events, got %d", timeline.EventCount)
	}

	// All events should be task_state kind.
	for _, evt := range timeline.Events {
		if evt.Kind != ReplayEventKindTaskState {
			t.Errorf("expected task_state kind, got %q", evt.Kind)
		}
		ts, ok := evt.Data.(TaskStateSummary)
		if !ok {
			t.Errorf("expected TaskStateSummary data, got %T", evt.Data)
			continue
		}
		if ts.TaskID != "task-1" {
			t.Errorf("expected task_id=task-1, got %q", ts.TaskID)
		}
	}

	// Verify ordering.
	if !timeline.Events[0].Timestamp.Equal(now) {
		t.Errorf("first event should be at created_at, got %v", timeline.Events[0].Timestamp)
	}
}

func TestBuildTimeline_ArtifactEvents(t *testing.T) {
	now := time.Now().UTC()
	arts := []*Artifact{
		{ID: "art-1", SandboxID: "sbx-1", WorkspaceID: "", Path: "out.txt", Kind: "file", CreatedAt: now.Add(2 * time.Second)},
		{ID: "art-2", SandboxID: "sbx-1", WorkspaceID: "", Path: "patch.diff", Kind: "diff", DiffSummary: "+3 -1", CreatedAt: now.Add(4 * time.Second)},
	}

	al := &mockArtifactLister{artifacts: arts}
	timeline := mustBuildTimeline(t, "sbx-1", "", &mockChunkLister{}, &mockTaskLister{}, al)

	if timeline.EventCount != 2 {
		t.Errorf("expected 2 artifact events, got %d", timeline.EventCount)
	}

	for _, evt := range timeline.Events {
		if evt.Kind != ReplayEventKindArtifact {
			t.Errorf("expected artifact kind, got %q", evt.Kind)
		}
		as, ok := evt.Data.(ArtifactSummary)
		if !ok {
			t.Errorf("expected ArtifactSummary data, got %T", evt.Data)
			continue
		}
		if as.ArtifactID != "art-1" && as.ArtifactID != "art-2" {
			t.Errorf("unexpected artifact_id %q", as.ArtifactID)
		}
	}
}

func TestBuildTimeline_MixedOrdering(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t1created := base
	t1started := base.Add(1 * time.Second)
	t1completed := base.Add(5 * time.Second)

	chunks := []*OutputChunk{
		{ID: "c1", SandboxID: "sbx-mix", Sequence: 1, Stream: "stdout", Data: "line1", Timestamp: base.Add(2 * time.Second)},
		{ID: "c2", SandboxID: "sbx-mix", Sequence: 2, Stream: "stdout", Data: "line2", Timestamp: base.Add(4 * time.Second)},
	}
	tasks := []*Task{
		{
			ID:          "t1",
			SandboxID:   "sbx-mix",
			Kind:        TaskKindCommand,
			State:       TaskStateSucceeded,
			CreatedAt:   t1created,
			StartedAt:   &t1started,
			CompletedAt: &t1completed,
		},
	}
	arts := []*Artifact{
		{ID: "a1", SandboxID: "sbx-mix", Path: "out.log", Kind: "log", CreatedAt: base.Add(6 * time.Second)},
	}

	timeline := mustBuildTimeline(t, "sbx-mix", "",
		&mockChunkLister{chunks: chunks},
		&mockTaskLister{tasks: tasks},
		&mockArtifactLister{artifacts: arts},
	)

	// Total events: 2 chunks + 3 task states + 1 artifact = 6
	if timeline.EventCount != 6 {
		t.Errorf("expected 6 events, got %d", timeline.EventCount)
	}

	// Strictly ordered.
	for i := 1; i < len(timeline.Events); i++ {
		if timeline.Events[i].Timestamp.Before(timeline.Events[i-1].Timestamp) {
			t.Errorf("events out of order at index %d: %v before %v",
				i, timeline.Events[i].Timestamp, timeline.Events[i-1].Timestamp)
		}
	}

	// StartTime should be the earliest, EndTime the latest.
	if !timeline.StartTime.Equal(base) {
		t.Errorf("expected StartTime=%v, got %v", base, timeline.StartTime)
	}
	expectedEnd := base.Add(6 * time.Second)
	if !timeline.EndTime.Equal(expectedEnd) {
		t.Errorf("expected EndTime=%v, got %v", expectedEnd, timeline.EndTime)
	}
}

func TestBuildTimeline_OverlappingTimestamps(t *testing.T) {
	now := time.Now().UTC()

	// Multiple events at the same timestamp — must not reorder relative order.
	chunks := []*OutputChunk{
		{ID: "c1", SandboxID: "sbx-ov", Sequence: 1, Stream: "stdout", Data: "a", Timestamp: now},
		{ID: "c2", SandboxID: "sbx-ov", Sequence: 2, Stream: "stdout", Data: "b", Timestamp: now},
	}

	timeline := mustBuildTimeline(t, "sbx-ov", "",
		&mockChunkLister{chunks: chunks},
		&mockTaskLister{},
		&mockArtifactLister{},
	)

	if timeline.EventCount != 2 {
		t.Errorf("expected 2 events, got %d", timeline.EventCount)
	}
	// Duration should be zero when all events have same timestamp.
	if timeline.Duration != 0 {
		t.Errorf("expected zero Duration for same-timestamp events, got %v", timeline.Duration)
	}
}

func TestBuildTimeline_WorkspaceIsolation(t *testing.T) {
	now := time.Now().UTC()

	tasks := []*Task{
		{ID: "t1", SandboxID: "sbx-1", WorkspaceID: "ws-A", Kind: TaskKindCommand, State: TaskStateSucceeded,
			CreatedAt: now},
		{ID: "t2", SandboxID: "sbx-1", WorkspaceID: "ws-B", Kind: TaskKindCommand, State: TaskStateSucceeded,
			CreatedAt: now},
	}

	// With ws-A filter — only t1's events.
	tl := &mockTaskLister{tasks: tasks}
	timeline := mustBuildTimeline(t, "sbx-1", "ws-A", &mockChunkLister{}, tl, &mockArtifactLister{})

	// t1 produces 1 event (created → queued; no started/completed).
	if timeline.EventCount != 1 {
		t.Errorf("workspace isolation: expected 1 event for ws-A, got %d", timeline.EventCount)
	}
}

func TestReplayTimeline_Summary(t *testing.T) {
	now := time.Now().UTC()
	end := now.Add(10 * time.Second)
	tl := &ReplayTimeline{
		SandboxID:  "sbx-s",
		StartTime:  now,
		EndTime:    end,
		Duration:   10 * time.Second,
		EventCount: 42,
		Events:     []ReplayEvent{{Timestamp: now, Kind: ReplayEventKindOutput}},
	}

	s := tl.Summary()
	if s.SandboxID != "sbx-s" {
		t.Errorf("expected sandbox_id sbx-s, got %q", s.SandboxID)
	}
	if s.EventCount != 42 {
		t.Errorf("expected 42 events, got %d", s.EventCount)
	}
	if s.Duration != 10*time.Second {
		t.Errorf("expected 10s duration, got %v", s.Duration)
	}
}
