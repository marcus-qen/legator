// Package server wires together all control-plane subsystems and exposes the
// HTTP server. main() builds a Server, calls ListenAndServe, done.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/alerts"
	"github.com/marcus-qen/legator/internal/controlplane/api"
	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/chat"
	"github.com/marcus-qen/legator/internal/controlplane/cloudconnectors"
	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/controlplane/config"
	"github.com/marcus-qen/legator/internal/controlplane/discovery"
	"github.com/marcus-qen/legator/internal/controlplane/events"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/llm"
	"github.com/marcus-qen/legator/internal/controlplane/metrics"
	"github.com/marcus-qen/legator/internal/controlplane/modeldock"
	"github.com/marcus-qen/legator/internal/controlplane/oidc"
	"github.com/marcus-qen/legator/internal/controlplane/policy"
	"github.com/marcus-qen/legator/internal/controlplane/session"
	"github.com/marcus-qen/legator/internal/controlplane/users"
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

	// Multi-user auth
	userStore          *users.Store
	sessionStore       *session.Store
	userAuth           auth.UserAuthenticator
	sessionCreator     auth.SessionCreator
	sessionValidator   auth.SessionValidator
	sessionDeleter     auth.SessionDeleter
	permissionResolver auth.UserPermissionResolver
	oidcProvider       *oidc.Provider

	// Policy
	policyStore      policy.PolicyManager
	policyPersistent *policy.PersistentStore

	// Webhook
	webhookNotifier *webhook.Notifier
	webhookStore    *webhook.Store

	// Alerts
	alertEngine *alerts.Engine
	alertStore  *alerts.Store

	// Events
	eventBus *events.Bus

	// LLM
	taskRunner        *llm.TaskRunner
	managedTaskRunner *llm.TaskRunner
	modelProviderMgr  *modeldock.ProviderManager
	modelDockStore    *modeldock.Store
	modelDockHandlers *modeldock.Handler

	cloudConnectorStore    *cloudconnectors.Store
	cloudConnectorHandlers *cloudconnectors.Handler

	discoveryStore    *discovery.Store
	discoveryHandlers *discovery.Handler

	// Templates
	pages *pageTemplates

	// HTTP
	httpServer *http.Server
}

type pageTemplate struct {
	tmpl      *template.Template
	rootBlock string
}

type pageTemplates struct {
	templates map[string]pageTemplate
}

func (pt *pageTemplates) Render(w io.Writer, page string, data interface{}) error {
	if pt == nil {
		return fmt.Errorf("templates not initialized")
	}
	entry, ok := pt.templates[page]
	if !ok {
		return fmt.Errorf("template %q not found", page)
	}
	root := entry.rootBlock
	if root == "" {
		root = "base"
	}
	return entry.tmpl.ExecuteTemplate(w, root, data)
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
	if s.cfg.ExternalURL != "" {
		s.tokenStore.SetServerURL(s.cfg.ExternalURL)
	}
	s.cmdTracker = cmdtracker.New(2 * time.Minute)
	s.initAudit()
	s.initApprovals()
	s.initWebhooks()
	s.initAlerts()
	s.initChat()
	s.initPolicy()
	s.initModelDock()
	s.initCloudConnectors()
	s.initDiscovery()
	s.initLLM()
	s.initHub()
	s.wireChatLLM()
	s.initAuth()
	s.loadTemplates()

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	var handler http.Handler = mux
	if s.authStore != nil || s.sessionValidator != nil {
		authMiddleware := auth.NewMiddleware(s.authStore, []string{
			"/healthz",
			"/version",
			"/api/v1/register",
			"/download/*",
			"/install.sh",
			"/ws/probe",
			"/login",
			"/logout",
			"/auth/oidc/login",
			"/auth/oidc/callback",
			"/static/*",
			"/site/*",
		})
		authMiddleware.SetSessionAuth(s.sessionValidator, s.permissionResolver)
		handler = authMiddleware.Wrap(handler)
	}

	s.httpServer = &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      handler,
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

	// Forward event bus events to webhooks
	go s.webhookForwarder(ctx)

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
	if s.alertEngine != nil {
		s.alertEngine.Stop()
	}
	if s.alertStore != nil {
		s.alertStore.Close()
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
	if s.modelDockStore != nil {
		s.modelDockStore.Close()
	}
	if s.cloudConnectorStore != nil {
		s.cloudConnectorStore.Close()
	}
	if s.discoveryStore != nil {
		s.discoveryStore.Close()
	}
	if s.userStore != nil {
		s.userStore.Close()
	}
	if s.sessionStore != nil {
		s.sessionStore.Close()
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

func (s *Server) initAlerts() {
	alertsDBPath := filepath.Join(s.cfg.DataDir, "alerts.db")
	store, err := alerts.NewStore(alertsDBPath)
	if err != nil {
		s.logger.Warn("cannot open alerts database, falling back to in-memory",
			zap.String("path", alertsDBPath), zap.Error(err))
		store, err = alerts.NewStore(":memory:")
		if err != nil {
			s.logger.Error("cannot initialize alerts store", zap.Error(err))
			return
		}
	}

	s.alertStore = store
	s.alertEngine = alerts.NewEngine(store, s.fleetMgr, s.webhookNotifier, s.eventBus, s.logger.Named("alerts"))
	s.alertEngine.Start()
	s.logger.Info("alerts engine initialized", zap.String("path", alertsDBPath))
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

func (s *Server) initModelDock() {
	envCfg := llm.ProviderConfig{
		Name:    os.Getenv("LEGATOR_LLM_PROVIDER"),
		BaseURL: os.Getenv("LEGATOR_LLM_BASE_URL"),
		APIKey:  os.Getenv("LEGATOR_LLM_API_KEY"),
		Model:   os.Getenv("LEGATOR_LLM_MODEL"),
	}
	s.modelProviderMgr = modeldock.NewProviderManager(envCfg)

	modelDockDBPath := filepath.Join(s.cfg.DataDir, "modeldock.db")
	if err := os.MkdirAll(s.cfg.DataDir, 0750); err != nil {
		s.logger.Warn("cannot create data dir, model dock disabled",
			zap.String("dir", s.cfg.DataDir), zap.Error(err))
		return
	}

	store, err := modeldock.NewStore(modelDockDBPath)
	if err != nil {
		s.logger.Warn("cannot open model dock database, model dock disabled",
			zap.String("path", modelDockDBPath), zap.Error(err))
		return
	}

	s.modelDockStore = store
	s.modelDockHandlers = modeldock.NewHandler(store, s.modelProviderMgr, s.envProfileFromEnv)
	if err := s.modelProviderMgr.SyncFromStore(store); err != nil && !errors.Is(err, modeldock.ErrNoActiveProvider) {
		s.logger.Warn("failed to sync model provider from store", zap.Error(err))
	}
	s.logger.Info("model dock store opened", zap.String("path", modelDockDBPath))
}

func (s *Server) initCloudConnectors() {
	cloudDBPath := filepath.Join(s.cfg.DataDir, "cloud.db")
	if err := os.MkdirAll(s.cfg.DataDir, 0750); err != nil {
		s.logger.Warn("cannot create data dir, cloud connectors disabled",
			zap.String("dir", s.cfg.DataDir), zap.Error(err))
		return
	}

	store, err := cloudconnectors.NewStore(cloudDBPath)
	if err != nil {
		s.logger.Warn("cannot open cloud connectors database, cloud connectors disabled",
			zap.String("path", cloudDBPath), zap.Error(err))
		return
	}

	s.cloudConnectorStore = store
	s.cloudConnectorHandlers = cloudconnectors.NewHandler(store, cloudconnectors.NewCLIAdapter())
	s.logger.Info("cloud connector store opened", zap.String("path", cloudDBPath))
}

func (s *Server) initDiscovery() {
	discoveryDBPath := filepath.Join(s.cfg.DataDir, "discovery.db")
	if err := os.MkdirAll(s.cfg.DataDir, 0750); err != nil {
		s.logger.Warn("cannot create data dir, discovery disabled",
			zap.String("dir", s.cfg.DataDir), zap.Error(err))
		return
	}

	store, err := discovery.NewStore(discoveryDBPath)
	if err != nil {
		s.logger.Warn("cannot open discovery database, falling back to in-memory",
			zap.String("path", discoveryDBPath), zap.Error(err))
		store, err = discovery.NewStore(":memory:")
		if err != nil {
			s.logger.Error("cannot initialize discovery store", zap.Error(err))
			return
		}
	}

	s.discoveryStore = store
	s.discoveryHandlers = discovery.NewHandler(store, discovery.NewScanner(), s.tokenStore)
	s.logger.Info("discovery store opened", zap.String("path", discoveryDBPath))
}

func (s *Server) initLLM() {
	if s.modelProviderMgr == nil {
		s.modelProviderMgr = modeldock.NewProviderManager(llm.ProviderConfig{
			Name:    os.Getenv("LEGATOR_LLM_PROVIDER"),
			BaseURL: os.Getenv("LEGATOR_LLM_BASE_URL"),
			APIKey:  os.Getenv("LEGATOR_LLM_API_KEY"),
			Model:   os.Getenv("LEGATOR_LLM_MODEL"),
		})
	}

	snapshot := s.modelProviderMgr.Snapshot()
	if snapshot.Provider != "" {
		s.logger.Info("LLM provider configured",
			zap.String("provider", snapshot.Provider),
			zap.String("model", snapshot.Model),
			zap.String("source", snapshot.Source),
		)
	} else {
		s.logger.Info("LLM provider manager initialized without active provider")
	}

	approvalWait := 2 * time.Minute
	if raw := os.Getenv("LEGATOR_TASK_APPROVAL_WAIT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			approvalWait = d
		}
	}

	taskProvider := s.modelProviderMgr.Provider(modeldock.FeatureTask, s.modelDockStore)

	// dispatch is a closure that will be set after hub init
	s.taskRunner = llm.NewTaskRunner(taskProvider, func(probeID string, cmd *protocol.CommandPayload) (*protocol.CommandResultPayload, error) {
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
	s.managedTaskRunner = s.taskRunner
}

func (s *Server) initHub() {
	s.hub = cpws.NewHub(s.logger.Named("ws"), func(probeID string, env protocol.Envelope) {
		s.handleProbeMessage(probeID, env)
	})
	s.hub.SetLifecycleHooks(func(probeID string) {
		previousStatus := ""
		if ps, ok := s.fleetMgr.Get(probeID); ok {
			previousStatus = ps.Status
		}

		if err := s.fleetMgr.SetOnline(probeID); err != nil {
			s.logger.Warn("failed to mark probe online on connect",
				zap.String("probe", probeID),
				zap.Error(err),
			)
		}

		now := time.Now().UTC()
		detail := map[string]string{"status": "online", "last_seen": now.Format(time.RFC3339)}
		if previousStatus == "offline" || previousStatus == "degraded" {
			reconnectedDetail := map[string]string{
				"status":          "online",
				"last_seen":       now.Format(time.RFC3339),
				"previous_status": previousStatus,
			}
			s.publishEvent(events.ProbeReconnected, probeID, fmt.Sprintf("Probe %s reconnected", probeID), reconnectedDetail)
		}
		s.publishEvent(events.ProbeConnected, probeID, fmt.Sprintf("Probe %s connected", probeID), detail)
	}, func(probeID string) {
		now := time.Now().UTC()
		s.publishEvent(events.ProbeDisconnected, probeID, fmt.Sprintf("Probe %s disconnected", probeID),
			map[string]string{"status": "degraded", "last_seen": now.Format(time.RFC3339)})
	})

	// Authenticate probes by validating their API key against fleet store.
	s.hub.SetAuthenticator(func(probeID, bearerToken string) bool {
		ps, ok := s.fleetMgr.Get(probeID)
		if !ok {
			return false // unknown probe
		}
		if ps.APIKey == "" {
			return false // probe has no key (shouldn't happen)
		}
		return ps.APIKey == bearerToken
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
	if s.modelProviderMgr == nil {
		return
	}

	dispatch := func(probeID string, cmd *protocol.CommandPayload) (*protocol.CommandResultPayload, error) {
		return s.dispatchAndWait(probeID, cmd)
	}

	probeProvider := s.modelProviderMgr.Provider(modeldock.FeatureProbeChat, s.modelDockStore)
	fleetProvider := s.modelProviderMgr.Provider(modeldock.FeatureFleetChat, s.modelDockStore)

	chatResponder := llm.NewChatResponder(probeProvider, dispatch, s.logger.Named("chat-llm"))
	fleetResponder := llm.NewFleetChatResponder(fleetProvider, s.fleetMgr, dispatch, s.logger.Named("fleet-chat-llm"))

	responder := func(probeID, userMessage string, history []chat.Message) (string, error) {
		llmHistory := make([]llm.ChatMessage, len(history))
		for i, m := range history {
			llmHistory[i] = llm.ChatMessage{Role: m.Role, Content: m.Content}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		if probeID == "fleet" {
			return fleetResponder.Respond(ctx, llmHistory, userMessage)
		}

		var inv *protocol.InventoryPayload
		var level protocol.CapabilityLevel = protocol.CapObserve
		if ps, ok := s.fleetMgr.Get(probeID); ok {
			inv = ps.Inventory
			level = ps.PolicyLevel
		}

		return chatResponder.Respond(ctx, probeID, llmHistory, userMessage, inv, level)
	}

	if s.chatStore != nil {
		s.chatStore.SetResponder(responder)
	} else {
		s.chatMgr.SetResponder(responder)
	}
	s.logger.Info("chat wired to LLM provider manager")
}

func (s *Server) initAuth() {
	if !s.cfg.AuthEnabled {
		return
	}

	if err := os.MkdirAll(s.cfg.DataDir, 0750); err != nil {
		s.logger.Warn("cannot create data dir", zap.Error(err))
		return
	}

	// API key store
	authDBPath := filepath.Join(s.cfg.DataDir, "auth.db")
	store, err := auth.NewKeyStore(authDBPath)
	if err != nil {
		s.logger.Warn("cannot open auth database",
			zap.String("path", authDBPath), zap.Error(err))
	} else {
		s.authStore = store
		s.logger.Info("auth store opened", zap.String("path", authDBPath))
	}

	// User store
	userDBPath := filepath.Join(s.cfg.DataDir, "users.db")
	userStore, err := users.NewStore(userDBPath)
	if err != nil {
		s.logger.Warn("cannot open user database",
			zap.String("path", userDBPath), zap.Error(err))
		return
	}
	s.userStore = userStore
	s.logger.Info("user store opened", zap.String("path", userDBPath))

	// Bootstrap admin user on first run
	if userStore.Count() == 0 {
		password := generateBootstrapPassword()
		admin, err := userStore.Create("admin", "Administrator", password, "admin")
		if err != nil {
			s.logger.Error("failed to create bootstrap admin", zap.Error(err))
		} else {
			s.logger.Info("bootstrap admin user created",
				zap.String("username", admin.Username),
				zap.String("password", password),
				zap.String("role", admin.Role),
			)
			fmt.Fprintf(os.Stderr, "\n"+
				"╔════════════════════════════════════════════╗\n"+
				"║  FIRST RUN — Admin credentials created     ║\n"+
				"║  Username: admin                           ║\n"+
				"║  Password: %-32s║\n"+
				"║  Change this password immediately!         ║\n"+
				"╚════════════════════════════════════════════╝\n\n", password)
		}
	}

	// Session store
	sessionDBPath := filepath.Join(s.cfg.DataDir, "sessions.db")
	sessionStore, err := session.NewStore(sessionDBPath, 24*time.Hour)
	if err != nil {
		s.logger.Warn("cannot open session database",
			zap.String("path", sessionDBPath), zap.Error(err))
		return
	}
	s.sessionStore = sessionStore
	s.logger.Info("session store opened", zap.String("path", sessionDBPath))

	// Wire adapters
	sessAdapter := &sessionAdapter{store: sessionStore, userStore: userStore}
	s.userAuth = &userAuthAdapter{store: userStore}
	s.sessionCreator = sessAdapter
	s.sessionValidator = sessAdapter
	s.sessionDeleter = sessAdapter
	s.permissionResolver = &roleResolver{}

	if s.cfg.OIDC.Enabled {
		provider, err := oidc.NewProvider(context.Background(), s.cfg.OIDC, s.logger)
		if err != nil {
			s.logger.Warn("failed to initialize oidc provider", zap.Error(err))
		} else {
			s.oidcProvider = provider
			s.oidcProvider.Auditor = s.auditRecorder()
			s.logger.Info("oidc provider enabled", zap.String("provider", provider.ProviderName()))
		}
	}

	// Start session cleanup goroutine
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if n, err := sessionStore.Cleanup(); err == nil && n > 0 {
				s.logger.Info("expired sessions cleaned", zap.Int("count", n))
			}
		}
	}()
}

func generateBootstrapPassword() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)[:24]
}

func (s *Server) loadTemplates() {
	tmplDir := filepath.Join("web", "templates")
	pt := &pageTemplates{templates: make(map[string]pageTemplate)}

	pages := []string{"fleet", "probe-detail", "chat", "fleet-chat", "approvals", "audit", "alerts", "model-dock", "cloud-connectors", "discovery"}
	for _, page := range pages {
		t, err := template.New("").Funcs(templateFuncs()).ParseFiles(
			filepath.Join(tmplDir, "_base.html"),
			filepath.Join(tmplDir, page+".html"),
		)
		if err != nil {
			s.logger.Warn("failed to load page template", zap.String("page", page), zap.Error(err))
			return
		}
		pt.templates[page] = pageTemplate{tmpl: t, rootBlock: "base"}
	}

	loginTemplate, err := template.New("").Funcs(templateFuncs()).ParseFiles(filepath.Join(tmplDir, "login.html"))
	if err != nil {
		s.logger.Warn("failed to load login template", zap.Error(err))
		return
	}
	pt.templates["login"] = pageTemplate{tmpl: loginTemplate, rootBlock: "login.html"}

	s.pages = pt
}

// ── Internal helpers ─────────────────────────────────────────

func (s *Server) envProfileFromEnv() *modeldock.Profile {
	provider := strings.TrimSpace(os.Getenv("LEGATOR_LLM_PROVIDER"))
	if provider == "" {
		return nil
	}
	baseURL := strings.TrimSpace(os.Getenv("LEGATOR_LLM_BASE_URL"))
	model := strings.TrimSpace(os.Getenv("LEGATOR_LLM_MODEL"))
	if baseURL == "" || model == "" {
		return nil
	}
	return &modeldock.Profile{
		ID:       modeldock.EnvProfileID,
		Name:     "Environment (LEGATOR_LLM_*)",
		Provider: provider,
		BaseURL:  baseURL,
		Model:    model,
		APIKey:   strings.TrimSpace(os.Getenv("LEGATOR_LLM_API_KEY")),
		Source:   modeldock.SourceEnv,
		IsActive: true,
	}
}

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

// offlineChecker runs the periodic offline detection and publishes events.
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
					s.publishEvent(events.ProbeOffline, ps.ID,
						fmt.Sprintf("Probe %s (%s) went offline", ps.ID, ps.Hostname),
						map[string]string{"status": "offline", "last_seen": ps.LastSeen.Format(time.RFC3339)})
				}
				lastOffline[ps.ID] = (ps.Status == "offline")
			}
		}
	}
}

// webhookForwarder subscribes to the event bus and forwards events to registered webhooks.
func (s *Server) webhookForwarder(ctx context.Context) {
	ch := s.eventBus.Subscribe("webhook-forwarder")
	defer s.eventBus.Unsubscribe("webhook-forwarder")

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			// Forward to webhook notifier (non-blocking, Notify spawns goroutines)
			s.webhookNotifier.Notify(string(evt.Type), evt.ProbeID, evt.Summary, evt.Detail)
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
