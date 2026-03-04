package sandbox

import (
	"sort"
	"time"
)

// ChunkLister is the interface for fetching output chunks for a sandbox.
// Satisfied by *StreamStore.
type ChunkLister interface {
	ListChunksBySandbox(sandboxID string, sinceSequence int64, limit int) ([]*OutputChunk, error)
}

// TaskLister is the interface for fetching tasks for a sandbox.
// Satisfied by *TaskStore.
type TaskLister interface {
	ListTasks(f TaskListFilter) ([]*Task, error)
}

// ArtifactLister is the interface for fetching artifacts for a sandbox.
// Satisfied by *ArtifactStore.
type ArtifactLister interface {
	ListArtifacts(f ArtifactListFilter) ([]*Artifact, error)
}

// BuildTimeline assembles the full ReplayTimeline for a sandbox session.
// It merges output chunks, task state changes, and artifact events into
// a single time-ordered slice of ReplayEvents.
func BuildTimeline(
	sandboxID, workspaceID string,
	chunks ChunkLister,
	tasks TaskLister,
	artifacts ArtifactLister,
) (*ReplayTimeline, error) {
	var events []ReplayEvent

	// ── 1. Output chunks ──────────────────────────────────────────────────────
	// Fetch in batches of 1000 until exhausted.
	var sinceSeq int64 = 0
	for {
		batch, err := chunks.ListChunksBySandbox(sandboxID, sinceSeq, 1000)
		if err != nil {
			return nil, err
		}
		for _, c := range batch {
			events = append(events, ReplayEvent{
				Timestamp: c.Timestamp,
				Kind:      ReplayEventKindOutput,
				Data:      c,
			})
			if c.Sequence > sinceSeq {
				sinceSeq = c.Sequence
			}
		}
		if len(batch) < 1000 {
			break
		}
	}

	// ── 2. Task state-change events ───────────────────────────────────────────
	taskList, err := tasks.ListTasks(TaskListFilter{
		SandboxID:   sandboxID,
		WorkspaceID: workspaceID,
		Limit:       500,
	})
	if err != nil {
		return nil, err
	}

	for _, t := range taskList {
		// Created → queued
		events = append(events, ReplayEvent{
			Timestamp: t.CreatedAt,
			Kind:      ReplayEventKindTaskState,
			Data: TaskStateSummary{
				TaskID:  t.ID,
				Kind:    t.Kind,
				ToState: TaskStateQueued,
			},
		})

		// Queued → running (StartedAt)
		if t.StartedAt != nil {
			events = append(events, ReplayEvent{
				Timestamp: *t.StartedAt,
				Kind:      ReplayEventKindTaskState,
				Data: TaskStateSummary{
					TaskID:    t.ID,
					Kind:      t.Kind,
					FromState: TaskStateQueued,
					ToState:   TaskStateRunning,
				},
			})
		}

		// Running → terminal (CompletedAt)
		if t.CompletedAt != nil && t.IsTerminal() {
			events = append(events, ReplayEvent{
				Timestamp: *t.CompletedAt,
				Kind:      ReplayEventKindTaskState,
				Data: TaskStateSummary{
					TaskID:    t.ID,
					Kind:      t.Kind,
					FromState: TaskStateRunning,
					ToState:   t.State,
				},
			})
		}
	}

	// ── 3. Artifact events ────────────────────────────────────────────────────
	artifactList, err := artifacts.ListArtifacts(ArtifactListFilter{
		SandboxID:   sandboxID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return nil, err
	}

	for _, a := range artifactList {
		events = append(events, ReplayEvent{
			Timestamp: a.CreatedAt,
			Kind:      ReplayEventKindArtifact,
			Data: ArtifactSummary{
				ArtifactID:  a.ID,
				Path:        a.Path,
				Kind:        a.Kind,
				Size:        a.Size,
				DiffSummary: a.DiffSummary,
			},
		})
	}

	// ── 4. Sort strictly by timestamp ─────────────────────────────────────────
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	// ── 5. Compute timeline boundaries ────────────────────────────────────────
	var startTime, endTime time.Time
	if len(events) > 0 {
		startTime = events[0].Timestamp
		endTime = events[len(events)-1].Timestamp
	}

	return &ReplayTimeline{
		SandboxID:  sandboxID,
		StartTime:  startTime,
		EndTime:    endTime,
		Duration:   endTime.Sub(startTime),
		EventCount: len(events),
		Events:     events,
	}, nil
}
