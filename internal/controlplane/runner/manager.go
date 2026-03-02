package runner

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// State models runner lifecycle states.
type State string

const (
	StateCreated   State = "created"
	StateRunning   State = "running"
	StateStopped   State = "stopped"
	StateDestroyed State = "destroyed"
)

// Audience scopes a run token to a single lifecycle operation.
type Audience string

const (
	AudienceRunnerStart   Audience = "runner:start"
	AudienceRunnerStop    Audience = "runner:stop"
	AudienceRunnerDestroy Audience = "runner:destroy"
)

// BackendKind controls where runner commands execute.
type BackendKind string

const (
	BackendHost    BackendKind = "host"
	BackendSandbox BackendKind = "sandbox"
)

const defaultRunTokenTTL = 2 * time.Minute

var (
	ErrSessionRequired          = errors.New("session context required")
	ErrAudienceRequired         = errors.New("run token audience required")
	ErrInvalidAudience          = errors.New("invalid run token audience")
	ErrRunnerIDRequired         = errors.New("runner_id is required")
	ErrRunnerNotFound           = errors.New("runner not found")
	ErrInvalidTransition        = errors.New("invalid runner lifecycle transition")
	ErrRunTokenRequired         = errors.New("run token is required")
	ErrRunTokenInvalid          = errors.New("run token is invalid")
	ErrRunTokenExpired          = errors.New("run token is expired")
	ErrRunTokenConsumed         = errors.New("run token already consumed")
	ErrRunTokenScope            = errors.New("run token scope rejected")
	ErrRunTokenSessionBound     = errors.New("run token session binding rejected")
	ErrInvalidBackend           = errors.New("invalid runner backend")
	ErrSandboxCommandRequired   = errors.New("sandbox command is required")
	ErrSandboxContractMalformed = errors.New("sandbox contract malformed")
	ErrBackendUnavailable       = errors.New("runner execution backend unavailable")
	ErrBackendStartFailed       = errors.New("runner execution start failed")
	ErrBackendStopFailed        = errors.New("runner execution stop failed")
	ErrBackendTeardownFailed    = errors.New("runner execution teardown failed")
)

// SandboxContract describes disposable sandbox execution.
type SandboxContract struct {
	Image          string   `json:"image,omitempty"`
	Command        []string `json:"command,omitempty"`
	TimeoutSeconds int64    `json:"timeout_seconds,omitempty"`
}

// Runner is the control-plane runner lifecycle projection.
type Runner struct {
	ID          string           `json:"id"`
	Label       string           `json:"label,omitempty"`
	JobID       string           `json:"job_id,omitempty"`
	Backend     BackendKind      `json:"backend,omitempty"`
	Sandbox     *SandboxContract `json:"sandbox,omitempty"`
	State       State            `json:"state"`
	CreatedBy   string           `json:"created_by,omitempty"`
	SessionID   string           `json:"session_id,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	DestroyedAt time.Time        `json:"destroyed_at,omitempty"`
}

// CreateRequest describes a runner create operation.
type CreateRequest struct {
	Label     string
	JobID     string
	Backend   BackendKind
	Sandbox   *SandboxContract
	CreatedBy string
	SessionID string
}

// IssueTokenRequest describes a run token issuance operation.
type IssueTokenRequest struct {
	RunnerID  string
	JobID     string
	Audience  Audience
	SessionID string
	TTL       time.Duration
}

// IssuedToken is the response contract returned to callers.
type IssuedToken struct {
	Token     string    `json:"run_token"`
	RunnerID  string    `json:"runner_id"`
	JobID     string    `json:"job_id,omitempty"`
	Audience  Audience  `json:"audience"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	TTL       int64     `json:"ttl_seconds"`
}

// LifecycleRequest describes start/stop/destroy operations.
type LifecycleRequest struct {
	RunnerID  string
	JobID     string
	RunToken  string
	SessionID string
}

// Config controls manager behaviour.
type Config struct {
	RunTokenTTL    time.Duration
	Now            func() time.Time
	IDGenerator    func() string
	TokenGenerator func() (string, error)
}

type runTokenRecord struct {
	Token      string
	RunnerID   string
	JobID      string
	Audience   Audience
	SessionID  string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	ConsumedAt *time.Time
}

// Manager keeps runner lifecycle and ephemeral run tokens in-memory.
type Manager struct {
	mu             sync.Mutex
	runners        map[string]*Runner
	tokens         map[string]*runTokenRecord
	runTokenTTL    time.Duration
	now            func() time.Time
	idGenerator    func() string
	tokenGenerator func() (string, error)
}

// NewManager constructs a manager with safe defaults.
func NewManager(cfg Config) *Manager {
	ttl := cfg.RunTokenTTL
	if ttl <= 0 {
		ttl = defaultRunTokenTTL
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	idGen := cfg.IDGenerator
	if idGen == nil {
		idGen = uuid.NewString
	}
	tokenGen := cfg.TokenGenerator
	if tokenGen == nil {
		tokenGen = generateRunToken
	}

	return &Manager{
		runners:        make(map[string]*Runner),
		tokens:         make(map[string]*runTokenRecord),
		runTokenTTL:    ttl,
		now:            nowFn,
		idGenerator:    idGen,
		tokenGenerator: tokenGen,
	}
}

// CreateRunner creates a runner in created state.
func (m *Manager) CreateRunner(req CreateRequest) (*Runner, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return nil, ErrSessionRequired
	}

	backend, err := normalizeBackend(req.Backend)
	if err != nil {
		return nil, err
	}
	sandbox, err := normalizeSandboxContract(backend, req.Sandbox)
	if err != nil {
		return nil, err
	}

	now := m.now()
	runner := &Runner{
		ID:        strings.TrimSpace(m.idGenerator()),
		Label:     strings.TrimSpace(req.Label),
		JobID:     strings.TrimSpace(req.JobID),
		Backend:   backend,
		Sandbox:   sandbox,
		State:     StateCreated,
		CreatedBy: strings.TrimSpace(req.CreatedBy),
		SessionID: sessionID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if runner.ID == "" {
		runner.ID = uuid.NewString()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.runners[runner.ID] = cloneRunner(runner)
	return cloneRunner(runner), nil
}

// IssueRunToken mints a short-lived, session-bound, single-use lifecycle token.
func (m *Manager) IssueRunToken(req IssueTokenRequest) (*IssuedToken, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return nil, ErrSessionRequired
	}
	runnerID := strings.TrimSpace(req.RunnerID)
	if runnerID == "" {
		return nil, ErrRunnerIDRequired
	}
	audience := normalizeAudience(req.Audience)
	if audience == "" {
		return nil, ErrAudienceRequired
	}
	if !isAllowedAudience(audience) {
		return nil, fmt.Errorf("%w: %s", ErrInvalidAudience, audience)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpiredLocked()

	r, ok := m.runners[runnerID]
	if !ok {
		return nil, ErrRunnerNotFound
	}

	jobID := strings.TrimSpace(req.JobID)
	runnerJobID := strings.TrimSpace(r.JobID)
	switch {
	case runnerJobID != "" && jobID == "":
		jobID = runnerJobID
	case runnerJobID != "" && jobID != runnerJobID:
		return nil, fmt.Errorf("%w: token job_id %q does not match runner job_id %q", ErrRunTokenScope, jobID, runnerJobID)
	}

	ttl := req.TTL
	if ttl <= 0 {
		ttl = m.runTokenTTL
	}
	now := m.now()
	expiresAt := now.Add(ttl)

	token, err := m.tokenGenerator()
	if err != nil {
		return nil, fmt.Errorf("generate run token: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("generate run token: empty token")
	}

	record := &runTokenRecord{
		Token:     token,
		RunnerID:  runnerID,
		JobID:     jobID,
		Audience:  audience,
		SessionID: sessionID,
		IssuedAt:  now,
		ExpiresAt: expiresAt,
	}
	m.tokens[token] = record

	return &IssuedToken{
		Token:     token,
		RunnerID:  runnerID,
		JobID:     jobID,
		Audience:  audience,
		IssuedAt:  now,
		ExpiresAt: expiresAt,
		TTL:       int64(ttl / time.Second),
	}, nil
}

// StartRunner transitions runner to running after token checks.
func (m *Manager) StartRunner(req LifecycleRequest) (*Runner, error) {
	if _, err := m.PrepareRunnerLifecycle(req, AudienceRunnerStart); err != nil {
		return nil, err
	}
	return m.CompleteRunnerLifecycle(req.RunnerID, StateRunning)
}

// StopRunner transitions runner to stopped after token checks.
func (m *Manager) StopRunner(req LifecycleRequest) (*Runner, error) {
	if _, err := m.PrepareRunnerLifecycle(req, AudienceRunnerStop); err != nil {
		return nil, err
	}
	return m.CompleteRunnerLifecycle(req.RunnerID, StateStopped)
}

// DestroyRunner transitions runner to destroyed after token checks.
func (m *Manager) DestroyRunner(req LifecycleRequest) (*Runner, error) {
	if _, err := m.PrepareRunnerLifecycle(req, AudienceRunnerDestroy); err != nil {
		return nil, err
	}
	return m.CompleteRunnerLifecycle(req.RunnerID, StateDestroyed)
}

// PrepareRunnerLifecycle validates and consumes a lifecycle token without mutating state.
func (m *Manager) PrepareRunnerLifecycle(req LifecycleRequest, audience Audience) (*Runner, error) {
	target, ok := audienceTarget(audience)
	if !ok {
		return nil, fmt.Errorf("%w: unsupported audience %s", ErrInvalidAudience, audience)
	}
	return m.prepareTransition(req, audience, target)
}

// CompleteRunnerLifecycle applies a previously prepared transition.
func (m *Manager) CompleteRunnerLifecycle(runnerID string, target State) (*Runner, error) {
	runnerID = strings.TrimSpace(runnerID)
	if runnerID == "" {
		return nil, ErrRunnerIDRequired
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	r, ok := m.runners[runnerID]
	if !ok {
		return nil, ErrRunnerNotFound
	}
	if !canTransition(r.State, target) {
		return nil, fmt.Errorf("%w: cannot move from %s to %s", ErrInvalidTransition, r.State, target)
	}

	now := m.now()
	r.State = target
	r.UpdatedAt = now
	if target == StateDestroyed {
		r.DestroyedAt = now
	}
	return cloneRunner(r), nil
}

// GetRunner returns the current runner projection.
func (m *Manager) GetRunner(runnerID string) (*Runner, error) {
	runnerID = strings.TrimSpace(runnerID)
	if runnerID == "" {
		return nil, ErrRunnerIDRequired
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runners[runnerID]
	if !ok {
		return nil, ErrRunnerNotFound
	}
	return cloneRunner(r), nil
}

func (m *Manager) prepareTransition(req LifecycleRequest, audience Audience, target State) (*Runner, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return nil, ErrSessionRequired
	}
	runnerID := strings.TrimSpace(req.RunnerID)
	if runnerID == "" {
		return nil, ErrRunnerIDRequired
	}
	rawToken := strings.TrimSpace(req.RunToken)
	if rawToken == "" {
		return nil, ErrRunTokenRequired
	}
	requestedJobID := strings.TrimSpace(req.JobID)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpiredLocked()

	r, ok := m.runners[runnerID]
	if !ok {
		return nil, ErrRunnerNotFound
	}

	runnerJobID := strings.TrimSpace(r.JobID)
	if requestedJobID != "" && runnerJobID != "" && requestedJobID != runnerJobID {
		return nil, fmt.Errorf("%w: lifecycle job_id %q does not match runner job_id %q", ErrRunTokenScope, requestedJobID, runnerJobID)
	}
	if err := m.consumeRunTokenLocked(rawToken, audience, runnerID, runnerJobID, sessionID); err != nil {
		return nil, err
	}
	if !canTransition(r.State, target) {
		return nil, fmt.Errorf("%w: cannot move from %s to %s", ErrInvalidTransition, r.State, target)
	}

	return cloneRunner(r), nil
}

func (m *Manager) consumeRunTokenLocked(rawToken string, audience Audience, runnerID, jobID, sessionID string) error {
	record, ok := m.tokens[rawToken]
	if !ok {
		return ErrRunTokenInvalid
	}
	now := m.now()

	if record.ConsumedAt != nil {
		return fmt.Errorf("%w: token was consumed at %s", ErrRunTokenConsumed, record.ConsumedAt.UTC().Format(time.RFC3339Nano))
	}
	if now.After(record.ExpiresAt) {
		t := now
		record.ConsumedAt = &t
		return fmt.Errorf("%w: token expired at %s", ErrRunTokenExpired, record.ExpiresAt.UTC().Format(time.RFC3339Nano))
	}
	if strings.TrimSpace(record.SessionID) != strings.TrimSpace(sessionID) {
		return fmt.Errorf("%w: token session does not match caller session", ErrRunTokenSessionBound)
	}
	if normalizeAudience(record.Audience) != normalizeAudience(audience) {
		return fmt.Errorf("%w: expected audience %s", ErrRunTokenScope, audience)
	}
	if strings.TrimSpace(record.RunnerID) != strings.TrimSpace(runnerID) {
		return fmt.Errorf("%w: token audience bound to runner %s", ErrRunTokenScope, record.RunnerID)
	}
	if strings.TrimSpace(record.JobID) != strings.TrimSpace(jobID) {
		return fmt.Errorf("%w: token audience bound to job %s", ErrRunTokenScope, strings.TrimSpace(record.JobID))
	}

	t := now
	record.ConsumedAt = &t
	return nil
}

func (m *Manager) pruneExpiredLocked() {
	now := m.now()
	for token, record := range m.tokens {
		if record == nil {
			delete(m.tokens, token)
			continue
		}
		if record.ConsumedAt != nil {
			if now.Sub(*record.ConsumedAt) > time.Hour {
				delete(m.tokens, token)
			}
			continue
		}
		if now.After(record.ExpiresAt.Add(time.Hour)) {
			delete(m.tokens, token)
		}
	}
}

func canTransition(current, target State) bool {
	switch target {
	case StateRunning:
		return current == StateCreated || current == StateStopped
	case StateStopped:
		return current == StateRunning
	case StateDestroyed:
		return current == StateCreated || current == StateRunning || current == StateStopped
	default:
		return false
	}
}

func audienceTarget(a Audience) (State, bool) {
	switch normalizeAudience(a) {
	case AudienceRunnerStart:
		return StateRunning, true
	case AudienceRunnerStop:
		return StateStopped, true
	case AudienceRunnerDestroy:
		return StateDestroyed, true
	default:
		return "", false
	}
}

func isAllowedAudience(a Audience) bool {
	switch normalizeAudience(a) {
	case AudienceRunnerStart, AudienceRunnerStop, AudienceRunnerDestroy:
		return true
	default:
		return false
	}
}

func normalizeAudience(a Audience) Audience {
	return Audience(strings.TrimSpace(string(a)))
}

func normalizeBackend(b BackendKind) (BackendKind, error) {
	switch BackendKind(strings.TrimSpace(string(b))) {
	case "", BackendHost:
		return BackendHost, nil
	case BackendSandbox:
		return BackendSandbox, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrInvalidBackend, strings.TrimSpace(string(b)))
	}
}

func normalizeSandboxContract(backend BackendKind, in *SandboxContract) (*SandboxContract, error) {
	if backend != BackendSandbox {
		return nil, nil
	}
	if in == nil {
		in = &SandboxContract{}
	}
	out := &SandboxContract{
		Image:          strings.TrimSpace(in.Image),
		TimeoutSeconds: in.TimeoutSeconds,
	}
	for _, part := range in.Command {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out.Command = append(out.Command, trimmed)
		}
	}
	if len(out.Command) == 0 {
		return nil, ErrSandboxCommandRequired
	}
	if out.TimeoutSeconds < 0 {
		return nil, fmt.Errorf("%w: timeout_seconds must be >= 0", ErrSandboxContractMalformed)
	}
	return out, nil
}

func cloneRunner(in *Runner) *Runner {
	if in == nil {
		return nil
	}
	copy := *in
	if in.Sandbox != nil {
		sandboxCopy := *in.Sandbox
		sandboxCopy.Command = append([]string(nil), in.Sandbox.Command...)
		copy.Sandbox = &sandboxCopy
	}
	return &copy
}

func generateRunToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "lgrun_" + hex.EncodeToString(raw), nil
}
