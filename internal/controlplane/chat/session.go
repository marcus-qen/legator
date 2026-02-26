package chat

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Message is a single chat message in a probe-specific conversation.
type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"` // user, assistant, system
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	CommandID string    `json:"command_id,omitempty"`
}

// Session stores the message history for one probe.
type Session struct {
	ProbeID   string    `json:"probe_id"`
	Messages  []Message `json:"messages"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	mu        sync.RWMutex
}

// ResponderFunc generates an assistant reply given a probe ID and user message.
// It receives the chat history (excluding the new user message) for context.
// If nil, the manager uses a placeholder responder.
type ResponderFunc func(probeID, userMessage string, history []Message) (string, error)

const llmUnavailableUserMessage = "I'm unable to process your request right now — the LLM provider is unavailable. Please try again shortly."

// Manager stores chat sessions keyed by probe ID.
type Manager struct {
	sessions    map[string]*Session
	mu          sync.RWMutex
	logger      *zap.Logger
	subscribers map[string]map[chan Message]struct{}
	responder   ResponderFunc
}

// NewManager creates a new chat session manager.
func NewManager(logger *zap.Logger) *Manager {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &Manager{
		sessions:    make(map[string]*Session),
		logger:      logger,
		subscribers: make(map[string]map[chan Message]struct{}),
	}
}

// GetOrCreate returns the session for probeID, creating it if needed.
func (m *Manager) GetOrCreate(probeID string) *Session {
	if probeID == "" {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[probeID]
	if ok {
		return s
	}

	now := time.Now().UTC()
	s = &Session{
		ProbeID:   probeID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	m.sessions[probeID] = s
	return s
}

// AddMessage appends a message to a probe session and fan-outs to subscribers.
func (m *Manager) AddMessage(probeID, role, content string) *Message {
	sess := m.GetOrCreate(probeID)
	if sess == nil {
		return nil
	}

	msg := Message{
		ID:        uuid.NewString(),
		Role:      role,
		Content:   content,
		Timestamp: time.Now().UTC(),
	}

	sess.mu.Lock()
	sess.Messages = append(sess.Messages, msg)
	sess.UpdatedAt = msg.Timestamp
	sess.mu.Unlock()

	m.publish(probeID, msg)
	return &msg
}

// GetMessages returns the most recent N messages for probeID.
// If limit <= 0, all messages are returned.
func (m *Manager) GetMessages(probeID string, limit int) []Message {
	m.mu.RLock()
	sess, ok := m.sessions[probeID]
	m.mu.RUnlock()
	if !ok || sess == nil {
		return nil
	}

	sess.mu.RLock()
	defer sess.mu.RUnlock()

	messages := make([]Message, len(sess.Messages))
	copy(messages, sess.Messages)

	if limit <= 0 || limit >= len(messages) {
		return messages
	}

	return messages[len(messages)-limit:]
}

// Subscribe returns a channel that receives new messages for probeID,
// and a cancel function to stop the subscription.
func (m *Manager) Subscribe(probeID string) (<-chan Message, func()) {
	if probeID == "" {
		c := make(chan Message)
		close(c)
		return c, func() {}
	}

	ch := make(chan Message, 32)
	m.GetOrCreate(probeID)

	m.mu.Lock()
	subs := m.subscribers[probeID]
	if subs == nil {
		subs = make(map[chan Message]struct{})
		m.subscribers[probeID] = subs
	}
	subs[ch] = struct{}{}
	m.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			m.mu.Lock()
			if subs, ok := m.subscribers[probeID]; ok {
				delete(subs, ch)
				if len(subs) == 0 {
					delete(m.subscribers, probeID)
				}
			}
			m.mu.Unlock()
			close(ch)
		})
	}

	return ch, cancel
}

func (m *Manager) publish(probeID string, msg Message) {
	m.mu.RLock()
	subs := m.subscribers[probeID]
	if len(subs) == 0 {
		m.mu.RUnlock()
		return
	}

	copyCh := make([]chan Message, 0, len(subs))
	for c := range subs {
		copyCh = append(copyCh, c)
	}
	m.mu.RUnlock()

	for _, c := range copyCh {
		select {
		case c <- msg:
		default:
			m.logger.Warn("dropping chat message for slow websocket subscriber",
				zap.String("probe_id", probeID),
				zap.String("message_id", msg.ID),
			)
		}
	}
}

// SetResponder sets the function used to generate assistant replies.
func (m *Manager) SetResponder(fn ResponderFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responder = fn
}

// respond generates an assistant reply using the configured responder or placeholder.
func (m *Manager) respond(probeID, content string) string {
	m.mu.RLock()
	fn := m.responder
	m.mu.RUnlock()
	if fn != nil {
		history := m.GetMessages(probeID, 0) // all history
		reply, err := fn(probeID, content, history)
		if err != nil {
			m.logger.Warn("chat responder unavailable", zap.String("probe_id", probeID), zap.Error(err))
			return llmUnavailableUserMessage
		}
		return reply
	}
	return chatReplyFor(content)
}
