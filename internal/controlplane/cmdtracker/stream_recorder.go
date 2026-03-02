package cmdtracker

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	_ "modernc.org/sqlite"
)

const (
	defaultStreamReplayLimit = 500
	maxStreamReplayLimit     = 5000
)

// StreamEventKind classifies command stream timeline events.
type StreamEventKind string

const (
	StreamEventOutput   StreamEventKind = "output"
	StreamEventPolicy   StreamEventKind = "policy"
	StreamEventApproval StreamEventKind = "approval"
	StreamEventDispatch StreamEventKind = "dispatch"
	StreamEventResult   StreamEventKind = "result"
	StreamEventJob      StreamEventKind = "job"
)

// StreamEvent is a persisted and replayable command stream record.
type StreamEvent struct {
	RequestID string          `json:"request_id"`
	Seq       int64           `json:"seq"`
	Kind      StreamEventKind `json:"kind"`
	Stream    string          `json:"stream,omitempty"`
	Data      string          `json:"data,omitempty"`
	ChunkSeq  int             `json:"chunk_seq,omitempty"`
	Final     bool            `json:"final,omitempty"`
	ExitCode  *int            `json:"exit_code,omitempty"`
	Meta      map[string]any  `json:"meta,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// StreamReplayQuery controls command stream replay/resume lookup.
type StreamReplayQuery struct {
	ResumeToken string
	LastSeq     int64
	Since       *time.Time
	Limit       int
}

// StreamReplayResult returns a replay page plus resume metadata.
type StreamReplayResult struct {
	Events        []StreamEvent `json:"events"`
	EarliestSeq   int64         `json:"earliest_seq"`
	LatestSeq     int64         `json:"latest_seq"`
	NextSeq       int64         `json:"next_seq"`
	HasMore       bool          `json:"has_more"`
	Truncated     bool          `json:"truncated"`
	MissedFromSeq int64         `json:"missed_from_seq,omitempty"`
	MissedToSeq   int64         `json:"missed_to_seq,omitempty"`
	ResumeToken   string        `json:"resume_token"`
}

// StreamRetention configures bounded durable storage.
type StreamRetention struct {
	MaxEventsPerRequest int
	MaxEventsTotal      int
	MaxAge              time.Duration
}

func normalizeStreamRetention(cfg StreamRetention) StreamRetention {
	if cfg.MaxEventsPerRequest <= 0 {
		cfg.MaxEventsPerRequest = 2000
	}
	if cfg.MaxEventsTotal <= 0 {
		cfg.MaxEventsTotal = 100000
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 24 * time.Hour
	}
	return cfg
}

// StreamSubscriber receives live timeline events for a request.
type StreamSubscriber struct {
	RequestID string
	Ch        chan StreamEvent
	afterSeq  int64
	done      chan struct{}
	once      sync.Once
}

func (s *StreamSubscriber) close() {
	s.once.Do(func() {
		close(s.done)
	})
}

type streamState struct {
	nextSeq       int64
	nextOutputSeq int
	pendingOutput map[int]protocol.OutputChunkPayload
}

// StreamRecorder persists command output and marker events and supports replay+resume.
type StreamRecorder struct {
	db        *sql.DB
	retention StreamRetention

	mu     sync.Mutex
	states map[string]*streamState
	subs   map[string][]*StreamSubscriber
}

type scanner interface {
	Scan(dest ...any) error
}

// NewStreamRecorder opens/creates a durable command stream database.
func NewStreamRecorder(dbPath string, retention StreamRetention) (*StreamRecorder, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open stream db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS command_stream_events (
		request_id    TEXT NOT NULL,
		seq           INTEGER NOT NULL,
		kind          TEXT NOT NULL,
		stream        TEXT NOT NULL DEFAULT '',
		data          TEXT NOT NULL DEFAULT '',
		chunk_seq     INTEGER NOT NULL DEFAULT 0,
		final         INTEGER NOT NULL DEFAULT 0,
		exit_code     INTEGER,
		metadata_json TEXT NOT NULL DEFAULT '{}',
		created_at    TEXT NOT NULL,
		PRIMARY KEY (request_id, seq)
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create command_stream_events: %w", err)
	}
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_command_stream_request_seq ON command_stream_events(request_id, seq)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_command_stream_created ON command_stream_events(created_at, request_id, seq)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_command_stream_request_chunk ON command_stream_events(request_id, kind, chunk_seq)`)

	r := &StreamRecorder{
		db:        db,
		retention: normalizeStreamRetention(retention),
		states:    make(map[string]*streamState),
		subs:      make(map[string][]*StreamSubscriber),
	}
	if err := r.pruneLocked(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return r, nil
}

func (r *StreamRecorder) Close() {
	if r == nil || r.db == nil {
		return
	}
	_ = r.db.Close()
}

// EncodeResumeToken creates a stable resume token for a request timeline position.
func EncodeResumeToken(requestID string, seq int64) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ""
	}
	if seq < 0 {
		seq = 0
	}
	return requestID + ":" + strconv.FormatInt(seq, 10)
}

// DecodeResumeToken parses a token created by EncodeResumeToken.
func DecodeResumeToken(token string) (requestID string, seq int64, err error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", 0, nil
	}
	idx := strings.LastIndex(token, ":")
	if idx <= 0 || idx >= len(token)-1 {
		return "", 0, fmt.Errorf("invalid resume token")
	}
	requestID = strings.TrimSpace(token[:idx])
	if requestID == "" {
		return "", 0, fmt.Errorf("invalid resume token")
	}
	seq, err = strconv.ParseInt(strings.TrimSpace(token[idx+1:]), 10, 64)
	if err != nil || seq < 0 {
		return "", 0, fmt.Errorf("invalid resume token")
	}
	return requestID, seq, nil
}

// AppendMarker appends a non-output command stream event.
func (r *StreamRecorder) AppendMarker(requestID string, kind StreamEventKind, data string, meta map[string]any) (*StreamEvent, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, nil
	}
	kind = StreamEventKind(strings.TrimSpace(string(kind)))
	if kind == "" {
		kind = StreamEventResult
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	evt, err := r.insertEventLocked(requestID, StreamEvent{
		RequestID: requestID,
		Kind:      kind,
		Data:      strings.TrimSpace(data),
		Meta:      cloneMeta(meta),
	})
	if err != nil {
		return nil, err
	}
	return evt, nil
}

// AppendOutputChunk appends an output chunk while enforcing in-order chunk sequencing.
// Out-of-order chunks are buffered until missing earlier chunks arrive.
func (r *StreamRecorder) AppendOutputChunk(chunk protocol.OutputChunkPayload) ([]StreamEvent, error) {
	if r == nil || r.db == nil {
		return nil, nil
	}
	requestID := strings.TrimSpace(chunk.RequestID)
	if requestID == "" {
		return nil, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	state, err := r.loadStateLocked(requestID)
	if err != nil {
		return nil, err
	}

	if chunk.Seq <= 0 {
		chunk.Seq = state.nextOutputSeq
	}
	if chunk.Seq < state.nextOutputSeq {
		return nil, nil
	}
	if _, exists := state.pendingOutput[chunk.Seq]; !exists {
		state.pendingOutput[chunk.Seq] = chunk
	}

	emitted := make([]StreamEvent, 0, 1)
	for {
		next, ok := state.pendingOutput[state.nextOutputSeq]
		if !ok {
			break
		}
		delete(state.pendingOutput, state.nextOutputSeq)

		event := StreamEvent{
			RequestID: requestID,
			Kind:      StreamEventOutput,
			Stream:    strings.TrimSpace(next.Stream),
			Data:      next.Data,
			ChunkSeq:  next.Seq,
			Final:     next.Final,
		}
		if next.Final {
			exitCode := next.ExitCode
			event.ExitCode = &exitCode
		}

		stored, err := r.insertEventLocked(requestID, event)
		if err != nil {
			return emitted, err
		}
		emitted = append(emitted, *stored)
		state.nextOutputSeq++
	}

	return emitted, nil
}

// ReplayAndSubscribe replays persisted events then returns a live subscription without gaps.
func (r *StreamRecorder) ReplayAndSubscribe(requestID string, query StreamReplayQuery, bufSize int) (StreamReplayResult, *StreamSubscriber, func(), error) {
	if r == nil || r.db == nil {
		return StreamReplayResult{}, nil, nil, nil
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return StreamReplayResult{}, nil, nil, nil
	}
	if bufSize <= 0 {
		bufSize = 256
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.replayLocked(requestID, query)
	if err != nil {
		return StreamReplayResult{}, nil, nil, err
	}
	afterSeq := result.NextSeq

	sub := &StreamSubscriber{
		RequestID: requestID,
		Ch:        make(chan StreamEvent, bufSize),
		afterSeq:  afterSeq,
		done:      make(chan struct{}),
	}
	r.subs[requestID] = append(r.subs[requestID], sub)

	cleanup := func() {
		sub.close()
		r.mu.Lock()
		defer r.mu.Unlock()
		subs := r.subs[requestID]
		for i, candidate := range subs {
			if candidate == sub {
				r.subs[requestID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		if len(r.subs[requestID]) == 0 {
			delete(r.subs, requestID)
		}
	}

	return result, sub, cleanup, nil
}

// Replay loads persisted stream events using resume cursors.
func (r *StreamRecorder) Replay(requestID string, query StreamReplayQuery) (StreamReplayResult, error) {
	if r == nil || r.db == nil {
		return StreamReplayResult{}, nil
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return StreamReplayResult{}, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return r.replayLocked(requestID, query)
}

func (r *StreamRecorder) replayLocked(requestID string, query StreamReplayQuery) (StreamReplayResult, error) {
	lastSeq, err := resolveReplayCursor(requestID, query)
	if err != nil {
		return StreamReplayResult{}, err
	}
	if lastSeq < 0 {
		lastSeq = 0
	}

	limit := query.Limit
	if limit <= 0 {
		limit = defaultStreamReplayLimit
	}
	if limit > maxStreamReplayLimit {
		limit = maxStreamReplayLimit
	}

	result := StreamReplayResult{Events: []StreamEvent{}}

	var (
		earliest sql.NullInt64
		latest   sql.NullInt64
	)
	if err := r.db.QueryRow(`SELECT MIN(seq), MAX(seq) FROM command_stream_events WHERE request_id = ?`, requestID).Scan(&earliest, &latest); err != nil {
		return result, err
	}
	if earliest.Valid {
		result.EarliestSeq = earliest.Int64
	}
	if latest.Valid {
		result.LatestSeq = latest.Int64
	}

	if result.EarliestSeq > 0 && lastSeq+1 < result.EarliestSeq {
		result.Truncated = true
		result.MissedFromSeq = lastSeq + 1
		result.MissedToSeq = result.EarliestSeq - 1
	}

	args := []any{requestID, lastSeq}
	querySQL := `SELECT seq, kind, stream, data, chunk_seq, final, exit_code, metadata_json, created_at
		FROM command_stream_events
		WHERE request_id = ? AND seq > ?`
	if query.Since != nil && !query.Since.IsZero() {
		querySQL += ` AND created_at >= ?`
		args = append(args, query.Since.UTC().Format(time.RFC3339Nano))
	}
	querySQL += ` ORDER BY seq ASC LIMIT ?`
	args = append(args, limit+1)

	rows, err := r.db.Query(querySQL, args...)
	if err != nil {
		return result, err
	}
	defer rows.Close()

	events := make([]StreamEvent, 0, limit+1)
	for rows.Next() {
		event, err := scanStreamEvent(requestID, rows)
		if err != nil {
			continue
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}

	if len(events) > limit {
		result.HasMore = true
		events = events[:limit]
	}
	result.Events = events
	result.NextSeq = lastSeq
	if len(events) > 0 {
		result.NextSeq = events[len(events)-1].Seq
	} else if result.LatestSeq > 0 && lastSeq > result.LatestSeq {
		result.NextSeq = result.LatestSeq
	}
	if result.NextSeq < 0 {
		result.NextSeq = 0
	}
	result.ResumeToken = EncodeResumeToken(requestID, result.NextSeq)
	return result, nil
}

func resolveReplayCursor(requestID string, query StreamReplayQuery) (int64, error) {
	lastSeq := query.LastSeq
	if lastSeq < 0 {
		lastSeq = 0
	}
	if strings.TrimSpace(query.ResumeToken) != "" {
		tokenReqID, seq, err := DecodeResumeToken(query.ResumeToken)
		if err != nil {
			return 0, err
		}
		if tokenReqID != "" && tokenReqID != requestID {
			return 0, fmt.Errorf("resume token request_id mismatch")
		}
		if seq > lastSeq {
			lastSeq = seq
		}
	}
	return lastSeq, nil
}

func (r *StreamRecorder) insertEventLocked(requestID string, evt StreamEvent) (*StreamEvent, error) {
	state, err := r.loadStateLocked(requestID)
	if err != nil {
		return nil, err
	}
	if state.nextSeq <= 0 {
		state.nextSeq = 1
	}
	evt.RequestID = requestID
	evt.Seq = state.nextSeq
	evt.CreatedAt = time.Now().UTC()
	if evt.Meta == nil {
		evt.Meta = map[string]any{}
	}
	metaJSON, _ := json.Marshal(evt.Meta)

	_, err = r.db.Exec(`INSERT INTO command_stream_events(
		request_id, seq, kind, stream, data, chunk_seq, final, exit_code, metadata_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		evt.RequestID,
		evt.Seq,
		string(evt.Kind),
		evt.Stream,
		evt.Data,
		evt.ChunkSeq,
		boolToInt(evt.Final),
		nullableInt(evt.ExitCode),
		string(metaJSON),
		evt.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, err
	}
	state.nextSeq++

	if err := r.pruneLocked(); err != nil {
		return nil, err
	}
	r.dispatchLocked(evt)
	copy := evt
	return &copy, nil
}

func (r *StreamRecorder) dispatchLocked(evt StreamEvent) {
	subs := append([]*StreamSubscriber(nil), r.subs[evt.RequestID]...)
	for _, sub := range subs {
		if evt.Seq <= sub.afterSeq {
			continue
		}
		select {
		case <-sub.done:
		case sub.Ch <- evt:
		default:
		}
	}
}

func (r *StreamRecorder) loadStateLocked(requestID string) (*streamState, error) {
	if existing, ok := r.states[requestID]; ok {
		return existing, nil
	}
	state := &streamState{nextSeq: 1, nextOutputSeq: 1, pendingOutput: make(map[int]protocol.OutputChunkPayload)}

	var maxSeq sql.NullInt64
	if err := r.db.QueryRow(`SELECT MAX(seq) FROM command_stream_events WHERE request_id = ?`, requestID).Scan(&maxSeq); err != nil {
		return nil, err
	}
	if maxSeq.Valid {
		state.nextSeq = maxSeq.Int64 + 1
	}

	var maxChunk sql.NullInt64
	if err := r.db.QueryRow(`SELECT MAX(chunk_seq) FROM command_stream_events WHERE request_id = ? AND kind = ?`, requestID, string(StreamEventOutput)).Scan(&maxChunk); err != nil {
		return nil, err
	}
	if maxChunk.Valid {
		state.nextOutputSeq = int(maxChunk.Int64) + 1
	}

	r.states[requestID] = state
	return state, nil
}

func (r *StreamRecorder) pruneLocked() error {
	if r == nil || r.db == nil {
		return nil
	}
	if r.retention.MaxAge > 0 {
		cutoff := time.Now().UTC().Add(-r.retention.MaxAge).Format(time.RFC3339Nano)
		if _, err := r.db.Exec(`DELETE FROM command_stream_events WHERE created_at < ?`, cutoff); err != nil {
			return err
		}
	}
	if r.retention.MaxEventsPerRequest > 0 {
		rows, err := r.db.Query(`SELECT request_id, MAX(seq) FROM command_stream_events GROUP BY request_id`)
		if err != nil {
			return err
		}
		deletes := make([]struct {
			requestID string
			cutoffSeq int64
		}, 0)
		for rows.Next() {
			var (
				requestID string
				maxSeq    sql.NullInt64
			)
			if err := rows.Scan(&requestID, &maxSeq); err != nil {
				_ = rows.Close()
				return err
			}
			if !maxSeq.Valid {
				continue
			}
			cutoffSeq := maxSeq.Int64 - int64(r.retention.MaxEventsPerRequest)
			if cutoffSeq > 0 {
				deletes = append(deletes, struct {
					requestID string
					cutoffSeq int64
				}{requestID: requestID, cutoffSeq: cutoffSeq})
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		for _, item := range deletes {
			if _, err := r.db.Exec(`DELETE FROM command_stream_events WHERE request_id = ? AND seq <= ?`, item.requestID, item.cutoffSeq); err != nil {
				return err
			}
		}
	}

	if r.retention.MaxEventsTotal > 0 {
		var total int
		if err := r.db.QueryRow(`SELECT COUNT(*) FROM command_stream_events`).Scan(&total); err != nil {
			return err
		}
		excess := total - r.retention.MaxEventsTotal
		if excess > 0 {
			if _, err := r.db.Exec(`DELETE FROM command_stream_events WHERE rowid IN (
				SELECT rowid FROM command_stream_events ORDER BY created_at ASC, request_id ASC, seq ASC LIMIT ?
			)`, excess); err != nil {
				return err
			}
		}
	}
	return nil
}

func scanStreamEvent(requestID string, s scanner) (StreamEvent, error) {
	var (
		event        StreamEvent
		kind         string
		createdAt    string
		metadataJSON string
		exitCode     sql.NullInt64
		finalInt     int
	)
	if err := s.Scan(&event.Seq, &kind, &event.Stream, &event.Data, &event.ChunkSeq, &finalInt, &exitCode, &metadataJSON, &createdAt); err != nil {
		return StreamEvent{}, err
	}
	event.RequestID = requestID
	event.Kind = StreamEventKind(strings.TrimSpace(kind))
	event.Final = finalInt == 1
	if exitCode.Valid {
		v := int(exitCode.Int64)
		event.ExitCode = &v
	}
	event.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if strings.TrimSpace(metadataJSON) != "" {
		_ = json.Unmarshal([]byte(metadataJSON), &event.Meta)
	}
	if event.Meta == nil {
		event.Meta = map[string]any{}
	}
	return event, nil
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func cloneMeta(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return map[string]any{}
	}
	clone := make(map[string]any, len(meta))
	for k, v := range meta {
		clone[k] = v
	}
	return clone
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
