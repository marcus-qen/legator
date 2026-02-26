// Package server wires together all control-plane subsystems and exposes the
// HTTP server. main() builds a Server, calls ListenAndServe, done.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/api"
	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/chat"
	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/controlplane/config"
	"github.com/marcus-qen/legator/internal/controlplane/events"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/llm"
	"github.com/marcus-qen/legator/internal/controlplane/metrics"
	"github.com/marcus-qen/legator/internal/controlplane/policy"
	"github.com/marcus-qen/legator/internal/controlplane/webhook"
	cpws "github.com/marcus-qen/legator/internal/controlplane/websocket"
	"github.com/marcus-qen/legator/internal/protocol"
	"github.com/marcus-qen/legator/internal/shared/signing"
	"go.uber.org/zap"
)

// Version info injected at build time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Server is the assembled control plane.
type Server struct {
	cfg    config.Config
	logger *zap.Logger

	// Core subsystems
	fleetMgr      fleet.Fleet
	fleetStore    *fleet.Store
	tokenStore    *api.TokenStore
	cmdTracker    *cmdtracker.Tracker
	approvalQueue *approval.Queue
	hub           *cpws.Hub

	// Persistence (nil = in-memory fallback)
	auditLog   *audit.Log
	auditStore *audit.Store
	chatMgr    *chat.Manager
	chatStore  *chat.Store
	authStore  *auth.KeyStore

	// Policy
	policyStore      policy.PolicyManager
	policyPersistent *policy.PersistentStore

	// Webhook
	webhookNotifier *webhook.Notifier
	webhookStore    *webhook.Store

	// Events
	eventBus *events.Bus

	// LLM
	taskRunner *llm.TaskRunner

	// Templates
	tmpl *template.Template

	// HTTP
	httpServer *http.Server
}

// New builds a fully-wired Server from config.
func New(cfg config.Config, logger *zap.Logger) (*Server, error) {
	s := &Server{
		cfg:    cfg,
		logger: logger,
	}

	s.eventBus = events.NewBus(256)

	if err := s.initFleet(); err != nil {
		return nil, err
	}
	s.tokenStore = api.NewTokenStore()
	s.cmdTracker = cmdtracker.New(2 * time.Minute)
	s.initAudit()
	s.initApprovals()
	s.initWebhooks()
	s.initChat()
	s.initPolicy()
	s.initLLM()
	s.initHub()
	s.wireChatLLM()
	s.initAuth()
	s.loadTemplates()

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.httpServer = &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return s, nil
}

// Run starts the server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	// Start approval reaper
	s.approvalQueue.StartReaper(30*time.Second, ctx.Done())

	// Start offline checker
	go s.offlineChecker(ctx)

	s.logger.Info("starting control plane",
		zap.String("addr", s.cfg.ListenAddr),
		zap.String("version", Version),
		zap.Bool("audit_persistent", s.auditStore != nil),
		zap.Bool("fleet_persistent", s.fleetStore != nil),
		zap.Bool("chat_persistent", s.chatStore != nil),
		zap.Bool("webhook_persistent", s.webhookStore != nil),
	)

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	s.logger.Info("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.httpServer.Shutdown(shutdownCtx)
}

// Close releases all resources.
func (s *Server) Close() {
	if s.fleetStore != nil {
		s.fleetStore.Close()
	}
	if s.auditStore != nil {
		s.auditStore.Close()
	}
	if s.chatStore != nil {
		s.chatStore.Close()
	}
	if s.webhookStore != nil {
		s.webhookStore.Close()
	}
	if s.authStore != nil {
		s.authStore.Close()
	}
	if s.policyPersistent != nil {
		s.policyPersistent.Close()
	}
}

// ── Init helpers ─────────────────────────────────────────────

func (s *Server) initFleet() error {
	fleetDBPath := filepath.Join(s.cfg.DataDir, "fleet.db")
	if err := os.MkdirAll(s.cfg.DataDir, 0750); err == nil {
		store, err := fleet.NewStore(fleetDBPath, s.logger.Named("fleet"))
		if err != nil {
			s.logger.Warn("cannot open fleet database, falling back to in-memory",
				zap.String("path", fleetDBPath), zap.Error(err))
			s.fleetMgr = fleet.NewManager(s.logger.Named("fleet"))
		} else {
			s.fleetStore = store
			s.fleetMgr = store
			s.logger.Info("fleet store opened", zap.String("path", fleetDBPath))
		}
	} else {
		s.logger.Warn("cannot create data dir, fleet will be in-memory only",
			zap.String("dir", s.cfg.DataDir), zap.Error(err))
		s.fleetMgr = fleet.NewManager(s.logger.Named("fleet"))
	}
	return nil
}

func (s *Server) initAudit() {
	auditDBPath := filepath.Join(s.cfg.DataDir, "audit.db")
	if err := os.MkdirAll(s.cfg.DataDir, 0750); err != nil {
		s.logger.Warn("cannot create data dir, audit log will be in-memory only",
			zap.String("dir", s.cfg.DataDir), zap.Error(err))
		s.auditLog = audit.NewLog(10000)
	} else {
		store, err := audit.NewStore(auditDBPath, 10000)
		if err != nil {
			s.logger.Warn("cannot open audit database, falling back to in-memory",
				zap.String("path", auditDBPath), zap.Error(err))
			s.auditLog = audit.NewLog(10000)
		} else {
			s.auditStore = store
			s.logger.Info("audit store opened", zap.String("path", auditDBPath))
		}
	}
}

func (s *Server) initApprovals() {
	s.approvalQueue = approval.NewQueue(15*time.Minute, 500)
	// Reaper will be started when Run() is called via context
	s.logger.Info("approval queue initialized", zap.Duration("ttl", 15*time.Minute))
}

func (s *Server) initWebhooks() {
	webhookDBPath := filepath.Join(s.cfg.DataDir, "webhook.db")
	if err := os.MkdirAll(s.cfg.DataDir, 0750); err == nil {
		store, err := webhook.NewStore(webhookDBPath)
		if err != nil {
			s.logger.Warn("cannot open webhook database, falling back to in-memory",
				zap.String("path", webhookDBPath), zap.Error(err))
			s.webhookNotifier = webhook.NewNotifier()
		} else {
			s.webhookStore = store
			s.webhookNotifier = store.Notifier()
			s.logger.Info("webhook store opened", zap.String("path", webhookDBPath))
		}
	} else {
		s.webhookNotifier = webhook.NewNotifier()
	}
}

func (s *Server) initChat() {
	chatDBPath := filepath.Join(s.cfg.DataDir, "chat.db")
	if err := os.MkdirAll(s.cfg.DataDir, 0750); err == nil {
		store, err := chat.NewStore(chatDBPath, s.logger.Named("chat"))
		if err != nil {
			s.logger.Warn("cannot open chat database, falling back to in-memory",
				zap.String("path", chatDBPath), zap.Error(err))
			s.chatMgr = chat.NewManager(s.logger.Named("chat"))
		} else {
			s.chatStore = store
			s.chatMgr = store.Manager()
			s.logger.Info("chat store opened", zap.String("path", chatDBPath))
		}
	} else {
		s.chatMgr = chat.NewManager(s.logger.Named("chat"))
	}
}

func (s *Server) initPolicy() {
	policyDBPath := filepath.Join(s.cfg.DataDir, "policy.db")
	if ps, err := policy.NewPersistentStore(policyDBPath); err != nil {
		s.logger.Warn("cannot open policy database, falling back to in-memory",
			zap.String("path", policyDBPath), zap.Error(err))
		s.policyStore = policy.NewStore()
	} else {
		s.policyPersistent = ps
		s.policyStore = ps
		s.logger.Info("policy store opened", zap.String("path", policyDBPath))
	}
}

func (s *Server) initLLM() {
	modelProvider := os.Getenv("LEGATOR_LLM_PROVIDER")
	if modelProvider == "" {
		return
	}

	providerCfg := llm.ProviderConfig{
		Name:    modelProvider,
		BaseURL: os.Getenv("LEGATOR_LLM_BASE_URL"),
		APIKey:  os.Getenv("LEGATOR_LLM_API_KEY"),
		Model:   os.Getenv("LEGATOR_LLM_MODEL"),
	}
	provider := llm.NewOpenAIProvider(providerCfg)
	s.logger.Info("LLM provider configured",
		zap.String("provider", providerCfg.Name),
		zap.String("model", providerCfg.Model),
	)

	approvalWait := 2 * time.Minute
	if raw := os.Getenv("LEGATOR_TASK_APPROVAL_WAIT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			approvalWait = d
		}
	}

	// dispatch is a closure that will be set after hub init
	s.taskRunner = llm.NewTaskRunner(provider, func(probeID string, cmd *protocol.CommandPayload) (*protocol.CommandResultPayload, error) {
		if ps, ok := s.fleetMgr.Get(probeID); ok && approval.NeedsApproval(cmd, ps.PolicyLevel) {
			risk := approval.ClassifyRisk(cmd)
			req, err := s.approvalQueue.Submit(probeID, cmd, "LLM task command", risk, "llm-task")
			if err != nil {
				return nil, fmt.Errorf("approval queue unavailable: %w", err)
			}
			s.emitAudit(audit.EventApprovalRequest, probeID, "llm-task",
				fmt.Sprintf("LLM command pending approval: %s (risk: %s)", cmd.Command, risk))

			decided, err := s.approvalQueue.WaitForDecision(req.ID, approvalWait)
			if err != nil {
				return nil, fmt.Errorf("approval required (id=%s): %w", req.ID, err)
			}
			s.emitAudit(audit.EventApprovalDecided, probeID, decided.DecidedBy,
				fmt.Sprintf("LLM approval %s for: %s", decided.Decision, cmd.Command))
			if decided.Decision != approval.DecisionApproved {
				return nil, fmt.Errorf("command not approved (id=%s, decision=%s)", decided.ID, decided.Decision)
			}
		}

		return s.dispatchAndWait(probeID, cmd)
	}, s.logger.Named("task"))
}

func (s *Server) initHub() {
	s.hub = cpws.NewHub(s.logger.Named("ws"), func(probeID string, env protocol.Envelope) {
		s.handleProbeMessage(probeID, env)
	})

	// Signing key: config file > env var > auto-generated
	signingKeyHex := s.cfg.SigningKey
	if signingKeyHex == "" {
		signingKeyHex = os.Getenv("LEGATOR_SIGNING_KEY")
	}
	var signingKey []byte
	if signingKeyHex != "" {
		var err error
		signingKey, err = hex.DecodeString(signingKeyHex)
		if err != nil || len(signingKey) < 32 {
			s.logger.Fatal("LEGATOR_SIGNING_KEY must be >= 64 hex chars (32 bytes)")
		}
		s.logger.Info("command signing enabled (key from environment)")
	} else {
		signingKey = make([]byte, 32)
		if _, err := rand.Read(signingKey); err != nil {
			s.logger.Fatal("failed to generate signing key", zap.Error(err))
		}
		s.logger.Info("command signing enabled (auto-generated key)",
			zap.String("key_hex", hex.EncodeToString(signingKey)))
	}
	s.hub.SetSigner(signing.NewSigner(signingKey))
}

func (s *Server) wireChatLLM() {
	if s.taskRunner == nil {
		return
	}

	chatResponder := llm.NewChatResponder(
		llm.NewOpenAIProvider(llm.ProviderConfig{
			Name:    os.Getenv("LEGATOR_LLM_PROVIDER"),
			BaseURL: os.Getenv("LEGATOR_LLM_BASE_URL"),
			APIKey:  os.Getenv("LEGATOR_LLM_API_KEY"),
			Model:   os.Getenv("LEGATOR_LLM_MODEL"),
		}),
		func(probeID string, cmd *protocol.CommandPayload) (*protocol.CommandResultPayload, error) {
			return s.dispatchAndWait(probeID, cmd)
		},
		s.logger.Named("chat-llm"),
	)

	responder := func(probeID, userMessage string, history []chat.Message) string {
		llmHistory := make([]llm.ChatMessage, len(history))
		for i, m := range history {
			llmHistory[i] = llm.ChatMessage{Role: m.Role, Content: m.Content}
		}

		var inv *protocol.InventoryPayload
		var level protocol.CapabilityLevel = protocol.CapObserve
		if ps, ok := s.fleetMgr.Get(probeID); ok {
			inv = ps.Inventory
			level = ps.PolicyLevel
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		reply, err := chatResponder.Respond(ctx, probeID, llmHistory, userMessage, inv, level)
		if err != nil {
			s.logger.Warn("chat LLM error", zap.String("probe", probeID), zap.Error(err))
			return fmt.Sprintf("LLM error: %s. Try again or use the command API directly.", err.Error())
		}
		return reply
	}

	if s.chatStore != nil {
		s.chatStore.SetResponder(responder)
	} else {
		s.chatMgr.SetResponder(responder)
	}
	s.logger.Info("chat wired to LLM provider")
}

func (s *Server) initAuth() {
	if os.Getenv("LEGATOR_AUTH") != "true" && os.Getenv("LEGATOR_AUTH") != "1" {
		return
	}
	authDBPath := filepath.Join(s.cfg.DataDir, "auth.db")
	if err := os.MkdirAll(s.cfg.DataDir, 0750); err == nil {
		store, err := auth.NewKeyStore(authDBPath)
		if err != nil {
			s.logger.Warn("cannot open auth database",
				zap.String("path", authDBPath), zap.Error(err))
		} else {
			s.authStore = store
			s.logger.Info("auth store opened", zap.String("path", authDBPath))
		}
	}
}

func (s *Server) loadTemplates() {
	tmplDir := filepath.Join("web", "templates")
	tmpl, err := template.New("").Funcs(templateFuncs()).ParseGlob(filepath.Join(tmplDir, "*.html"))
	if err != nil {
		s.logger.Warn("failed to load templates, UI will show fallback", zap.Error(err))
		return
	}
	s.tmpl = tmpl
}

// ── Internal helpers ─────────────────────────────────────────

func (s *Server) emitAudit(typ audit.EventType, probeID, actor, summary string) {
	if s.auditStore != nil {
		s.auditStore.Emit(typ, probeID, actor, summary)
	} else {
		s.auditLog.Emit(typ, probeID, actor, summary)
	}
}

func (s *Server) recordAudit(evt audit.Event) {
	if s.auditStore != nil {
		s.auditStore.Record(evt)
	} else {
		s.auditLog.Record(evt)
	}
}

func (s *Server) queryAudit(f audit.Filter) []audit.Event {
	if s.auditStore != nil {
		return s.auditStore.Query(f)
	}
	return s.auditLog.Query(f)
}

func (s *Server) countAudit() int {
	if s.auditStore != nil {
		return s.auditStore.Count()
	}
	return s.auditLog.Count()
}

func (s *Server) auditRecorder() api.AuditRecorder {
	if s.auditStore != nil {
		return s.auditStore
	}
	return s.auditLog
}

func (s *Server) metricsAuditCounter() metrics.AuditCounter {
	if s.auditStore != nil {
		return s.auditStore
	}
	return s.auditLog
}

// publishEvent emits an event to the bus for SSE subscribers.
func (s *Server) publishEvent(typ events.EventType, probeID, summary string, detail interface{}) {
	s.eventBus.Publish(events.Event{
		Type:    typ,
		ProbeID: probeID,
		Summary: summary,
		Detail:  detail,
	})
}

// dispatchAndWait sends a command to a probe and waits for the result.
func (s *Server) dispatchAndWait(probeID string, cmd *protocol.CommandPayload) (*protocol.CommandResultPayload, error) {
	pending := s.cmdTracker.Track(cmd.RequestID, probeID, cmd.Command, cmd.Level)
	if err := s.hub.SendTo(probeID, protocol.MsgCommand, *cmd); err != nil {
		s.cmdTracker.Cancel(cmd.RequestID)
		return nil, err
	}
	timeout := cmd.Timeout + 5*time.Second
	if timeout < 10*time.Second {
		timeout = 35 * time.Second
	}
	select {
	case result := <-pending.Result:
		return result, nil
	case <-time.After(timeout):
		s.cmdTracker.Cancel(cmd.RequestID)
		return nil, fmt.Errorf("timeout waiting for probe response")
	}
}

// offlineChecker runs the periodic offline detection + webhook notifications.
func (s *Server) offlineChecker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	lastOffline := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.fleetMgr.MarkOffline(60 * time.Second)
			for _, ps := range s.fleetMgr.List() {
				if ps.Status == "offline" && !lastOffline[ps.ID] {
					s.webhookNotifier.Notify("probe.offline", ps.ID,
						fmt.Sprintf("Probe %s (%s) went offline", ps.ID, ps.Hostname),
						map[string]string{"last_seen": ps.LastSeen.Format(time.RFC3339)})
				}
				lastOffline[ps.ID] = (ps.Status == "offline")
			}
		}
	}
}

// hubConnectedAdapter adapts Hub.Connected() []string to metrics.HubStats.Connected() int.
type hubConnectedAdapter struct {
	hub *cpws.Hub
}

func (a *hubConnectedAdapter) Connected() int {
	return len(a.hub.Connected())
}
