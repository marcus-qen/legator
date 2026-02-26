package chat

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	"go.uber.org/zap"
)

// Store provides persistent chat backed by SQLite.
// Wraps the in-memory Manager — reads from memory, writes to both.
type Store struct {
	db  *sql.DB
	mgr *Manager
}

// NewStore opens (or creates) a SQLite-backed chat store.
func NewStore(dbPath string, logger *zap.Logger) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS chat_messages (
		id         TEXT PRIMARY KEY,
		probe_id   TEXT NOT NULL,
		role       TEXT NOT NULL,
		content    TEXT NOT NULL,
		command_id TEXT NOT NULL DEFAULT '',
		timestamp  TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_chat_probe ON chat_messages(probe_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_chat_ts ON chat_messages(timestamp)`)

	s := &Store{db: db, mgr: NewManager(logger)}

	if err := s.loadAll(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

// Manager returns the underlying in-memory Manager.
func (s *Store) Manager() *Manager {
	return s.mgr
}

// ── Delegated reads ─────────────────────────────────────────

func (s *Store) GetMessages(probeID string, limit int) []Message {
	return s.mgr.GetMessages(probeID, limit)
}

func (s *Store) Subscribe(probeID string) (<-chan Message, func()) {
	return s.mgr.Subscribe(probeID)
}

// ── Mutations (memory + disk) ───────────────────────────────

// AddMessage appends a message and persists it.
func (s *Store) AddMessage(probeID, role, content string) *Message {
	msg := s.mgr.AddMessage(probeID, role, content)
	if msg != nil {
		_ = s.persist(probeID, msg)
	}
	return msg
}

// SetResponder delegates to the underlying Manager.
func (s *Store) SetResponder(fn ResponderFunc) {
	s.mgr.SetResponder(fn)
}

// Close shuts down the store.
func (s *Store) Close() error {
	return s.db.Close()
}

// ── Internal persistence ────────────────────────────────────

func (s *Store) persist(probeID string, msg *Message) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO chat_messages (id, probe_id, role, content, command_id, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)`,
		msg.ID,
		probeID,
		msg.Role,
		msg.Content,
		msg.CommandID,
		msg.Timestamp.Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) loadAll() error {
	rows, err := s.db.Query(`SELECT id, probe_id, role, content, command_id, timestamp FROM chat_messages ORDER BY timestamp ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, probeID, role, content, commandID, ts string
		)
		if err := rows.Scan(&id, &probeID, &role, &content, &commandID, &ts); err != nil {
			continue
		}

		msg := Message{
			ID:        id,
			Role:      role,
			Content:   content,
			CommandID: commandID,
		}
		msg.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)

		sess := s.mgr.GetOrCreate(probeID)
		if sess == nil {
			continue
		}
		sess.mu.Lock()
		sess.Messages = append(sess.Messages, msg)
		sess.UpdatedAt = msg.Timestamp
		sess.mu.Unlock()
	}

	return rows.Err()
}

// MessageCount returns the total persisted message count.
func (s *Store) MessageCount() int {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM chat_messages").Scan(&count); err != nil {
		return 0
	}
	return count
}

// ── HTTP Handlers (delegated, using persistent AddMessage) ──

// HandleGetMessages serves chat history with persistence.
func (s *Store) HandleGetMessages(w http.ResponseWriter, r *http.Request) {
	s.mgr.HandleGetMessages(w, r)
}

// HandleSendMessage handles sending a message with persistent storage.
// Overrides the Manager's handler to use Store.AddMessage for persistence.
func (s *Store) HandleSendMessage(w http.ResponseWriter, r *http.Request) {
	probeID := parseProbeID(r.URL.Path)
	if probeID == "" {
		http.Error(w, `{"error":"missing probe id"}`, http.StatusBadRequest)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	content := strings.TrimSpace(req.Content)
	if content == "" {
		http.Error(w, `{"error":"message content required"}`, http.StatusBadRequest)
		return
	}

	if s.AddMessage(probeID, "user", content) == nil {
		http.Error(w, `{"error":"failed to persist user message"}`, http.StatusInternalServerError)
		return
	}

	reply := s.mgr.respond(probeID, content)
	assistant := s.AddMessage(probeID, "assistant", reply)
	if assistant == nil {
		http.Error(w, `{"error":"failed to generate assistant reply"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(assistant)
}

// HandleChatWS handles WebSocket chat with persistent storage.
func (s *Store) HandleChatWS(w http.ResponseWriter, r *http.Request) {
	probeID := r.URL.Query().Get("probe_id")
	if probeID == "" {
		http.Error(w, `{"error":"missing probe_id"}`, http.StatusBadRequest)
		return
	}

	conn, err := chatUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.mgr.logger.Error("upgrade failed", zap.Error(err), zap.String("probe_id", probeID))
		return
	}
	defer conn.Close()

	messages, cancel := s.Subscribe(probeID)
	defer cancel()

	_ = s.AddMessage(probeID, "system", fmt.Sprintf("Connected to chat for probe %s", probeID))

	done := make(chan struct{})
	go func() {
		for msg := range messages {
			if err := conn.WriteJSON(msg); err != nil {
				break
			}
		}
		close(done)
	}()

	for {
		var req chatRequest
		if err := conn.ReadJSON(&req); err != nil {
			break
		}
		content := strings.TrimSpace(req.Content)
		if content == "" {
			continue
		}
		if s.AddMessage(probeID, "user", content) == nil {
			break
		}
		reply := s.mgr.respond(probeID, content)
		if s.AddMessage(probeID, "assistant", reply) == nil {
			break
		}
	}

	select {
	case <-done:
	default:
		_ = conn.Close()
	}
}
