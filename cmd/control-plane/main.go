// Legator Control Plane — the central brain that manages probe agents.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/api"
	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/controlplane/audit"
	"github.com/marcus-qen/legator/internal/controlplane/chat"
	"github.com/marcus-qen/legator/internal/controlplane/cmdtracker"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"github.com/marcus-qen/legator/internal/controlplane/llm"
	"github.com/marcus-qen/legator/internal/controlplane/metrics"
	"github.com/marcus-qen/legator/internal/controlplane/policy"
	cpws "github.com/marcus-qen/legator/internal/controlplane/websocket"
	"github.com/marcus-qen/legator/internal/protocol"
	"github.com/marcus-qen/legator/internal/shared/signing"
	"go.uber.org/zap"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Template types for fleet UI
type FleetSummary struct {
	Online   int
	Offline  int
	Degraded int
	Total    int
}

type FleetPageData struct {
	Probes  []*fleet.ProbeState
	Summary FleetSummary
	Version string
	Commit  string
}

type ProbePageData struct {
	Probe  *fleet.ProbeState
	Uptime string
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"statusClass":    templateStatusClass,
		"humanizeStatus": templateHumanizeStatus,
		"formatLastSeen": formatLastSeen,
		"humanBytes":     humanBytes,
	}
}

func main() {
	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()

	cfg, err := loadConfig()
	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err))
	}

	// Load templates
	tmplDir := filepath.Join("web", "templates")
	tmpl, err := template.New("").Funcs(templateFuncs()).ParseGlob(filepath.Join(tmplDir, "*.html"))
	if err != nil {
		logger.Warn("failed to load templates, UI will show fallback", zap.Error(err))
		tmpl = nil
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Core components — persistent fleet state when data dir is available
	var fleetMgr fleet.Fleet
	var fleetStore *fleet.Store
	fleetDBPath := filepath.Join(cfg.DataDir, "fleet.db")
	if err := os.MkdirAll(cfg.DataDir, 0750); err == nil {
		store, err := fleet.NewStore(fleetDBPath, logger.Named("fleet"))
		if err != nil {
			logger.Warn("cannot open fleet database, falling back to in-memory",
				zap.String("path", fleetDBPath), zap.Error(err))
			fleetMgr = fleet.NewManager(logger.Named("fleet"))
		} else {
			fleetStore = store
			fleetMgr = store
			logger.Info("fleet store opened", zap.String("path", fleetDBPath))
			defer fleetStore.Close()
		}
	} else {
		logger.Warn("cannot create data dir, fleet will be in-memory only",
			zap.String("dir", cfg.DataDir), zap.Error(err))
		fleetMgr = fleet.NewManager(logger.Named("fleet"))
	}
	tokenStore := api.NewTokenStore()
	cmdTracker := cmdtracker.New(2 * time.Minute)

	// Audit log: prefer SQLite-backed, fall back to in-memory
	var auditLog *audit.Log
	var auditStore *audit.Store
	auditDBPath := filepath.Join(cfg.DataDir, "audit.db")
	if err := os.MkdirAll(cfg.DataDir, 0750); err != nil {
		logger.Warn("cannot create data dir, audit log will be in-memory only",
			zap.String("dir", cfg.DataDir), zap.Error(err))
		auditLog = audit.NewLog(10000)
	} else {
		store, err := audit.NewStore(auditDBPath, 10000)
		if err != nil {
			logger.Warn("cannot open audit database, falling back to in-memory",
				zap.String("path", auditDBPath), zap.Error(err))
			auditLog = audit.NewLog(10000)
		} else {
			auditStore = store
			logger.Info("audit store opened", zap.String("path", auditDBPath))
			defer auditStore.Close()
		}
	}

	// Helper: emit audit event (works with either store or log)
	emitAudit := func(typ audit.EventType, probeID, actor, summary string) {
		if auditStore != nil {
			auditStore.Emit(typ, probeID, actor, summary)
		} else {
			auditLog.Emit(typ, probeID, actor, summary)
		}
	}
	recordAudit := func(evt audit.Event) {
		if auditStore != nil {
			auditStore.Record(evt)
		} else {
			auditLog.Record(evt)
		}
	}
	queryAudit := func(f audit.Filter) []audit.Event {
		if auditStore != nil {
			return auditStore.Query(f)
		}
		return auditLog.Query(f)
	}
	countAudit := func() int {
		if auditStore != nil {
			return auditStore.Count()
		}
		return auditLog.Count()
	}

	// Approval queue: 15-minute TTL, 500 max pending
	approvalQueue := approval.NewQueue(15*time.Minute, 500)
	approvalQueue.StartReaper(30*time.Second, ctx.Done())
	logger.Info("approval queue started", zap.Duration("ttl", 15*time.Minute))

	var hub *cpws.Hub

	// LLM provider (configured via env vars)
	var taskRunner *llm.TaskRunner
	if modelProvider := os.Getenv("LEGATOR_LLM_PROVIDER"); modelProvider != "" {
		providerCfg := llm.ProviderConfig{
			Name:    modelProvider,
			BaseURL: os.Getenv("LEGATOR_LLM_BASE_URL"),
			APIKey:  os.Getenv("LEGATOR_LLM_API_KEY"),
			Model:   os.Getenv("LEGATOR_LLM_MODEL"),
		}
		provider := llm.NewOpenAIProvider(providerCfg)
		logger.Info("LLM provider configured",
			zap.String("provider", providerCfg.Name),
			zap.String("model", providerCfg.Model),
		)

		approvalWait := 2 * time.Minute
		if raw := os.Getenv("LEGATOR_TASK_APPROVAL_WAIT"); raw != "" {
			if d, err := time.ParseDuration(raw); err == nil && d > 0 {
				approvalWait = d
			}
		}

		dispatch := func(probeID string, cmd *protocol.CommandPayload) (*protocol.CommandResultPayload, error) {
			if ps, ok := fleetMgr.Get(probeID); ok && approval.NeedsApproval(cmd, ps.PolicyLevel) {
				risk := approval.ClassifyRisk(cmd)
				req, err := approvalQueue.Submit(probeID, cmd, "LLM task command", risk, "llm-task")
				if err != nil {
					return nil, fmt.Errorf("approval queue unavailable: %w", err)
				}
				emitAudit(audit.EventApprovalRequest, probeID, "llm-task", fmt.Sprintf("LLM command pending approval: %s (risk: %s)", cmd.Command, risk))

				decided, err := approvalQueue.WaitForDecision(req.ID, approvalWait)
				if err != nil {
					return nil, fmt.Errorf("approval required (id=%s): %w", req.ID, err)
				}
				emitAudit(audit.EventApprovalDecided, probeID, decided.DecidedBy, fmt.Sprintf("LLM approval %s for: %s", decided.Decision, cmd.Command))
				if decided.Decision != approval.DecisionApproved {
					return nil, fmt.Errorf("command not approved (id=%s, decision=%s)", decided.ID, decided.Decision)
				}
			}

			pending := cmdTracker.Track(cmd.RequestID, probeID, cmd.Command, cmd.Level)
			if err := hub.SendTo(probeID, protocol.MsgCommand, *cmd); err != nil {
				cmdTracker.Cancel(cmd.RequestID)
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
				cmdTracker.Cancel(cmd.RequestID)
				return nil, fmt.Errorf("timeout waiting for probe response")
			}
		}
		taskRunner = llm.NewTaskRunner(provider, dispatch, logger.Named("task"))
	}

	hub = cpws.NewHub(logger.Named("ws"), func(probeID string, env protocol.Envelope) {
		handleProbeMessage(fleetMgr, emitAudit, recordAudit, cmdTracker, hub, logger, probeID, env)
	})

	signingKeyHex := os.Getenv("LEGATOR_SIGNING_KEY")
	var signingKey []byte
	if signingKeyHex != "" {
		var err error
		signingKey, err = hex.DecodeString(signingKeyHex)
		if err != nil || len(signingKey) < 32 {
			logger.Fatal("LEGATOR_SIGNING_KEY must be >= 64 hex chars (32 bytes)")
		}
		logger.Info("command signing enabled (key from environment)")
	} else {
		signingKey = make([]byte, 32)
		if _, err := rand.Read(signingKey); err != nil {
			logger.Fatal("failed to generate signing key", zap.Error(err))
		}
		logger.Info("command signing enabled (auto-generated key)",
			zap.String("key_hex", hex.EncodeToString(signingKey)))
	}
	hub.SetSigner(signing.NewSigner(signingKey))

	// Start offline checker
	go offlineChecker(ctx, fleetMgr)

	chatMgr := chat.NewManager(logger.Named("chat"))

	// Wire chat to LLM if task runner is available
	if taskRunner != nil {
		chatResponder := llm.NewChatResponder(
			llm.NewOpenAIProvider(llm.ProviderConfig{
				Name:    os.Getenv("LEGATOR_LLM_PROVIDER"),
				BaseURL: os.Getenv("LEGATOR_LLM_BASE_URL"),
				APIKey:  os.Getenv("LEGATOR_LLM_API_KEY"),
				Model:   os.Getenv("LEGATOR_LLM_MODEL"),
			}),
			func(probeID string, cmd *protocol.CommandPayload) (*protocol.CommandResultPayload, error) {
				pending := cmdTracker.Track(cmd.RequestID, probeID, cmd.Command, cmd.Level)
				if err := hub.SendTo(probeID, protocol.MsgCommand, *cmd); err != nil {
					cmdTracker.Cancel(cmd.RequestID)
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
					cmdTracker.Cancel(cmd.RequestID)
					return nil, fmt.Errorf("timeout waiting for probe response")
				}
			},
			logger.Named("chat-llm"),
		)

		chatMgr.SetResponder(func(probeID, userMessage string, history []chat.Message) string {
			// Convert chat.Message to llm.ChatMessage
			llmHistory := make([]llm.ChatMessage, len(history))
			for i, m := range history {
				llmHistory[i] = llm.ChatMessage{Role: m.Role, Content: m.Content}
			}

			// Get probe context
			var inv *protocol.InventoryPayload
			var level protocol.CapabilityLevel = protocol.CapObserve
			if ps, ok := fleetMgr.Get(probeID); ok {
				inv = ps.Inventory
				level = ps.PolicyLevel
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			reply, err := chatResponder.Respond(ctx, probeID, llmHistory, userMessage, inv, level)
			if err != nil {
				logger.Warn("chat LLM error", zap.String("probe", probeID), zap.Error(err))
				return fmt.Sprintf("LLM error: %s. Try again or use the command API directly.", err.Error())
			}
			return reply
		})
		logger.Info("chat wired to LLM provider")
	}

	policyStore := policy.NewStore()

	mux := http.NewServeMux()

	// ── Health + version ─────────────────────────────────────
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"version": version, "commit": commit, "date": date,
		})
	})

	// ── Fleet API ────────────────────────────────────────────
	mux.HandleFunc("GET /api/v1/probes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fleetMgr.List())
	})
	mux.HandleFunc("GET /api/v1/probes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ps, ok := fleetMgr.Get(id)
		if !ok {
			http.Error(w, `{"error":"probe not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ps)
	})
	mux.HandleFunc("POST /api/v1/probes/{id}/command", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ps, ok := fleetMgr.Get(id)
		if !ok {
			http.Error(w, `{"error":"probe not found"}`, http.StatusNotFound)
			return
		}

		var cmd protocol.CommandPayload
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
			return
		}
		if cmd.RequestID == "" {
			cmd.RequestID = fmt.Sprintf("cmd-%d", time.Now().UnixNano()%100000)
		}

		// Check if this command needs approval
		if approval.NeedsApproval(&cmd, ps.PolicyLevel) {
			req, err := approvalQueue.Submit(id, &cmd, "Manual command dispatch", approval.ClassifyRisk(&cmd), "api")
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"approval queue: %s"}`, err.Error()), http.StatusServiceUnavailable)
				return
			}
			emitAudit(audit.EventApprovalRequest, id, "api",
				fmt.Sprintf("Approval required for: %s (risk: %s)", cmd.Command, approval.ClassifyRisk(&cmd)))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":      "pending_approval",
				"approval_id": req.ID,
				"risk_level":  req.RiskLevel,
				"expires_at":  req.ExpiresAt,
				"message":     "Command requires human approval. Use POST /api/v1/approvals/{id}/decide to approve or deny.",
			})
			return
		}

		wantWait := r.URL.Query().Get("wait") == "true" || r.URL.Query().Get("wait") == "1"
		wantStream := r.URL.Query().Get("stream") == "true" || r.URL.Query().Get("stream") == "1"
		if wantStream {
			cmd.Stream = true
		}

		var pending *cmdtracker.PendingCommand
		if wantWait {
			pending = cmdTracker.Track(cmd.RequestID, id, cmd.Command, ps.PolicyLevel)
		}

		if err := hub.SendTo(id, protocol.MsgCommand, cmd); err != nil {
			if pending != nil {
				cmdTracker.Cancel(cmd.RequestID)
			}
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
			return
		}

		emitAudit(audit.EventCommandSent, id, "api", fmt.Sprintf("Command dispatched: %s", cmd.Command))

		if !wantWait {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status":     "dispatched",
				"request_id": cmd.RequestID,
			})
			return
		}

		timeout := 30 * time.Second
		if cmd.Timeout > 0 {
			timeout = cmd.Timeout + 5*time.Second
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case result := <-pending.Result:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(result)
		case <-timer.C:
			cmdTracker.Cancel(cmd.RequestID)
			http.Error(w, `{"error":"timeout waiting for probe response"}`, http.StatusGatewayTimeout)
		case <-r.Context().Done():
			cmdTracker.Cancel(cmd.RequestID)
		}
	})

	// ── Registration ─────────────────────────────────────────
	// Build audit recorder for register handlers
	var auditRecorder api.AuditRecorder
	if auditStore != nil {
		auditRecorder = auditStore
	} else {
		auditRecorder = auditLog
	}
	mux.HandleFunc("POST /api/v1/register", api.HandleRegisterWithAudit(tokenStore, fleetMgr, auditRecorder, logger.Named("register")))
	mux.HandleFunc("POST /api/v1/tokens", api.HandleGenerateTokenWithAudit(tokenStore, auditRecorder, logger.Named("tokens")))

	// ── Metrics (Prometheus-compatible) ──────────────────────
	var metricsAuditCounter metrics.AuditCounter
	if auditStore != nil {
		metricsAuditCounter = auditStore
	} else {
		metricsAuditCounter = auditLog
	}
	metricsCollector := metrics.NewCollector(
		fleetMgr,
		&hubConnectedAdapter{hub: hub},
		approvalQueue,
		metricsAuditCounter,
	)
	mux.HandleFunc("GET /api/v1/metrics", metricsCollector.Handler())

	// ── Fleet summary ────────────────────────────────────────
	mux.HandleFunc("GET /api/v1/fleet/summary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"counts":            fleetMgr.Count(),
			"connected":         hub.Connected(),
			"pending_approvals": approvalQueue.PendingCount(),
		})
	})

	// ── Probe tags ──────────────────────────────────────────
	mux.HandleFunc("PUT /api/v1/probes/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body struct {
			Tags []string `json:"tags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
			return
		}
		if err := fleetMgr.SetTags(id, body.Tags); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusNotFound)
			return
		}
		emitAudit(audit.EventPolicyChanged, id, "api", fmt.Sprintf("Tags set: %v", body.Tags))
		ps, _ := fleetMgr.Get(id)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"probe_id": id, "tags": ps.Tags})
	})

	mux.HandleFunc("GET /api/v1/fleet/tags", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tags": fleetMgr.TagCounts()})
	})

	mux.HandleFunc("GET /api/v1/fleet/by-tag/{tag}", func(w http.ResponseWriter, r *http.Request) {
		tag := r.PathValue("tag")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fleetMgr.ListByTag(tag))
	})

	// ── Group command (dispatch to all probes with a tag) ───
	mux.HandleFunc("POST /api/v1/fleet/by-tag/{tag}/command", func(w http.ResponseWriter, r *http.Request) {
		tag := r.PathValue("tag")
		probes := fleetMgr.ListByTag(tag)
		if len(probes) == 0 {
			http.Error(w, `{"error":"no probes with that tag"}`, http.StatusNotFound)
			return
		}

		var cmd protocol.CommandPayload
		if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
			http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
			return
		}

		results := make([]map[string]string, 0, len(probes))
		for _, ps := range probes {
			rid := fmt.Sprintf("grp-%s-%d", ps.ID[:8], time.Now().UnixNano()%100000)
			c := cmd
			c.RequestID = rid
			if err := hub.SendTo(ps.ID, protocol.MsgCommand, c); err != nil {
				results = append(results, map[string]string{
					"probe_id": ps.ID, "status": "error", "error": err.Error(),
				})
			} else {
				results = append(results, map[string]string{
					"probe_id": ps.ID, "status": "dispatched", "request_id": rid,
				})
			}
		}

		emitAudit(audit.EventCommandSent, tag, "api",
			fmt.Sprintf("Group command to %d probes (tag=%s): %s", len(probes), tag, cmd.Command))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag":     tag,
			"total":   len(probes),
			"results": results,
		})
	})

	// ── Policy templates ────────────────────────────────────
	mux.HandleFunc("GET /api/v1/policies", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(policyStore.List())
	})

	mux.HandleFunc("GET /api/v1/policies/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		tpl, ok := policyStore.Get(id)
		if !ok {
			http.Error(w, `{"error":"policy template not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tpl)
	})

	mux.HandleFunc("POST /api/v1/policies", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name        string                   `json:"name"`
			Description string                   `json:"description"`
			Level       protocol.CapabilityLevel `json:"level"`
			Allowed     []string                 `json:"allowed"`
			Blocked     []string                 `json:"blocked"`
			Paths       []string                 `json:"paths"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
			return
		}
		if body.Name == "" {
			http.Error(w, `{"error":"name required"}`, http.StatusBadRequest)
			return
		}
		tpl := policyStore.Create(body.Name, body.Description, body.Level, body.Allowed, body.Blocked, body.Paths)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(tpl)
	})

	mux.HandleFunc("DELETE /api/v1/policies/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := policyStore.Delete(id); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Apply a policy template to a probe (sends policy update over WebSocket)
	mux.HandleFunc("POST /api/v1/probes/{id}/apply-policy/{policyId}", func(w http.ResponseWriter, r *http.Request) {
		probeID := r.PathValue("id")
		policyID := r.PathValue("policyId")

		_, ok := fleetMgr.Get(probeID)
		if !ok {
			http.Error(w, `{"error":"probe not found"}`, http.StatusNotFound)
			return
		}
		tpl, ok := policyStore.Get(policyID)
		if !ok {
			http.Error(w, `{"error":"policy template not found"}`, http.StatusNotFound)
			return
		}

		// Update fleet manager state
		_ = fleetMgr.SetPolicy(probeID, tpl.Level)

		// Push policy to probe
		pol := tpl.ToPolicy()
		if err := hub.SendTo(probeID, protocol.MsgPolicyUpdate, pol); err != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status": "applied_locally",
				"note":   "probe offline, policy saved but not pushed",
			})
			return
		}

		emitAudit(audit.EventPolicyChanged, probeID, "api",
			fmt.Sprintf("Policy %s (%s) applied", tpl.Name, tpl.ID))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":    "applied",
			"probe_id":  probeID,
			"policy_id": policyID,
			"level":     string(tpl.Level),
		})
	})

	// ── Approval queue API ───────────────────────────────────
	mux.HandleFunc("GET /api/v1/approvals", func(w http.ResponseWriter, r *http.Request) {
		limitStr := r.URL.Query().Get("limit")
		limit := 50
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			limit = v
		}

		status := r.URL.Query().Get("status")
		w.Header().Set("Content-Type", "application/json")

		if status == "pending" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"approvals":     approvalQueue.Pending(),
				"pending_count": approvalQueue.PendingCount(),
			})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"approvals":     approvalQueue.All(limit),
			"pending_count": approvalQueue.PendingCount(),
		})
	})

	mux.HandleFunc("GET /api/v1/approvals/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		req, ok := approvalQueue.Get(id)
		if !ok {
			http.Error(w, `{"error":"approval request not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(req)
	})

	mux.HandleFunc("POST /api/v1/approvals/{id}/decide", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		var body struct {
			Decision  string `json:"decision"`   // "approved" or "denied"
			DecidedBy string `json:"decided_by"` // who is deciding
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		if body.Decision == "" || body.DecidedBy == "" {
			http.Error(w, `{"error":"decision and decided_by are required"}`, http.StatusBadRequest)
			return
		}

		req, err := approvalQueue.Decide(id, approval.Decision(body.Decision), body.DecidedBy)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
			return
		}

		emitAudit(audit.EventApprovalDecided, req.ProbeID, body.DecidedBy,
			fmt.Sprintf("Approval %s for: %s", body.Decision, req.Command.Command))

		// If approved, dispatch the command now
		if req.Decision == approval.DecisionApproved {
			if err := hub.SendTo(req.ProbeID, protocol.MsgCommand, *req.Command); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"approved but dispatch failed: %s"}`, err.Error()), http.StatusBadGateway)
				return
			}
			emitAudit(audit.EventCommandSent, req.ProbeID, body.DecidedBy,
				fmt.Sprintf("Approved command dispatched: %s", req.Command.Command))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  string(req.Decision),
			"request": req,
		})
	})

	// ── Audit log ────────────────────────────────────────────
	mux.HandleFunc("GET /api/v1/audit", func(w http.ResponseWriter, r *http.Request) {
		probeID := r.URL.Query().Get("probe_id")
		limitStr := r.URL.Query().Get("limit")
		limit := 50
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			limit = v
		}
		events := queryAudit(audit.Filter{ProbeID: probeID, Limit: limit})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "total": countAudit()})
	})

	// ── Task execution (LLM-powered) ─────────────────────────
	mux.HandleFunc("POST /api/v1/probes/{id}/task", func(w http.ResponseWriter, r *http.Request) {
		if taskRunner == nil {
			http.Error(w, `{"error":"no LLM provider configured. Set LEGATOR_LLM_PROVIDER, LEGATOR_LLM_BASE_URL, LEGATOR_LLM_API_KEY, LEGATOR_LLM_MODEL"}`, http.StatusServiceUnavailable)
			return
		}

		id := r.PathValue("id")
		ps, ok := fleetMgr.Get(id)
		if !ok {
			http.Error(w, `{"error":"probe not found"}`, http.StatusNotFound)
			return
		}

		var req struct {
			Task string `json:"task"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Task == "" {
			http.Error(w, `{"error":"task is required"}`, http.StatusBadRequest)
			return
		}

		logger.Info("task submitted",
			zap.String("probe", id),
			zap.String("task", req.Task),
		)

		emitAudit(audit.EventCommandSent, id, "llm-task", fmt.Sprintf("Task submitted: %s", req.Task))

		result, err := taskRunner.Run(r.Context(), id, req.Task, ps.Inventory, ps.PolicyLevel)
		if err != nil {
			logger.Warn("task execution error", zap.String("probe", id), zap.Error(err))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})

	// ── Pending commands ─────────────────────────────────────
	mux.HandleFunc("GET /api/v1/commands/pending", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pending":   cmdTracker.ListPending(),
			"in_flight": cmdTracker.InFlight(),
		})
	})

	// ── WebSocket endpoint for probes ────────────────────────
	// Probe update
	mux.HandleFunc("POST /api/v1/probes/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		_, ok := fleetMgr.Get(id)
		if !ok {
			http.Error(w, `{"error":"probe not found"}`, http.StatusNotFound)
			return
		}

		var upd protocol.UpdatePayload
		if err := json.NewDecoder(r.Body).Decode(&upd); err != nil {
			http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
			return
		}
		if upd.URL == "" {
			http.Error(w, `{"error":"url is required"}`, http.StatusBadRequest)
			return
		}

		if err := hub.SendTo(id, protocol.MsgUpdate, upd); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
			return
		}

		emitAudit(audit.EventCommandSent, id, "api",
			fmt.Sprintf("Update dispatched: %s → %s", upd.Version, upd.URL))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "dispatched",
			"version": upd.Version,
		})
	})

	// Chat API
	mux.HandleFunc("GET /api/v1/probes/{id}/chat", chatMgr.HandleGetMessages)
	mux.HandleFunc("POST /api/v1/probes/{id}/chat", chatMgr.HandleSendMessage)
	mux.HandleFunc("GET /ws/chat", chatMgr.HandleChatWS)

	// SSE endpoint for streaming command output
	mux.HandleFunc("GET /api/v1/commands/{requestId}/stream", func(w http.ResponseWriter, r *http.Request) {
		requestID := r.PathValue("requestId")
		if requestID == "" {
			http.Error(w, `{"error":"request_id required"}`, http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, `{"error":"streaming not supported"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		sub, cleanup := hub.SubscribeStream(requestID, 256)
		defer cleanup()

		for {
			select {
			case <-r.Context().Done():
				return
			case chunk := <-sub.Ch:
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
				if chunk.Final {
					return
				}
			}
		}
	})

	mux.HandleFunc("GET /ws/probe", hub.HandleProbeWS)

	// ── Static assets ────────────────────────────────────────
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join("web", "static")))))

	// ── Fleet UI (root) ──────────────────────────────────────
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if tmpl == nil {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Legator Control Plane</title></head>
<body>
<h1>Legator Control Plane</h1>
<p>Version: %s (%s)</p>
<p><a href="/api/v1/probes">Fleet API</a> | <a href="/api/v1/fleet/summary">Summary</a> | <a href="/api/v1/approvals?status=pending">Approvals</a></p>
</body></html>`, version, commit)
			return
		}

		probes := fleetMgr.List()
		sort.Slice(probes, func(i, j int) bool {
			lhs := strings.ToLower(probes[i].Hostname)
			if lhs == "" {
				lhs = probes[i].ID
			}
			rhs := strings.ToLower(probes[j].Hostname)
			if rhs == "" {
				rhs = probes[j].ID
			}
			return lhs < rhs
		})

		counts := fleetMgr.Count()
		data := FleetPageData{
			Probes: probes,
			Summary: FleetSummary{
				Online:   counts["online"],
				Offline:  counts["offline"],
				Degraded: counts["degraded"],
				Total:    len(probes),
			},
			Version: version,
			Commit:  commit,
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "fleet.html", data); err != nil {
			logger.Error("failed to render fleet page", zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	})

	// ── Probe detail UI ──────────────────────────────────────
	mux.HandleFunc("GET /probe/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ps, ok := fleetMgr.Get(id)
		if !ok {
			ps = &fleet.ProbeState{
				ID:          id,
				Status:      "offline",
				PolicyLevel: protocol.CapObserve,
			}
		}

		if tmpl == nil {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<h1>Probe: %s</h1><p>Status: %s</p>`, id, ps.Status)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := ProbePageData{
			Probe:  ps,
			Uptime: calculateUptime(ps.Registered),
		}
		if err := tmpl.ExecuteTemplate(w, "probe-detail.html", data); err != nil {
			logger.Error("failed to render probe detail", zap.String("probe", id), zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	})

	// ── Chat UI ─────────────────────────────────────────────
	mux.HandleFunc("GET /probe/{id}/chat", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ps, ok := fleetMgr.Get(id)
		if !ok {
			ps = &fleet.ProbeState{
				ID:          id,
				Status:      "offline",
				PolicyLevel: protocol.CapObserve,
			}
		}

		if tmpl == nil {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<h1>Chat: %s</h1><p>Template not loaded</p>`, id)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := ProbePageData{
			Probe:  ps,
			Uptime: calculateUptime(ps.Registered),
		}
		if err := tmpl.ExecuteTemplate(w, "chat.html", data); err != nil {
			logger.Error("failed to render chat", zap.String("probe", id), zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	})

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	logger.Info("starting control plane",
		zap.String("addr", cfg.ListenAddr),
		zap.String("version", version),
		zap.Bool("audit_persistent", auditStore != nil),
		zap.Bool("fleet_persistent", fleetStore != nil),
	)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("server error", zap.Error(err))
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", zap.Error(err))
	}
}

func handleProbeMessage(
	fm fleet.Fleet,
	emitAudit func(audit.EventType, string, string, string),
	recordAudit func(audit.Event),
	ct *cmdtracker.Tracker,
	wsHub *cpws.Hub,
	logger *zap.Logger,
	probeID string,
	env protocol.Envelope,
) {
	switch env.Type {
	case protocol.MsgHeartbeat:
		data, _ := json.Marshal(env.Payload)
		var hb protocol.HeartbeatPayload
		if err := json.Unmarshal(data, &hb); err != nil {
			logger.Warn("bad heartbeat payload", zap.String("probe", probeID), zap.Error(err))
			return
		}
		if err := fm.Heartbeat(probeID, &hb); err != nil {
			fm.Register(probeID, "", "", "")
			_ = fm.Heartbeat(probeID, &hb)
			emitAudit(audit.EventProbeRegistered, probeID, "system", "Auto-registered via heartbeat")
		}

	case protocol.MsgInventory:
		data, _ := json.Marshal(env.Payload)
		var inv protocol.InventoryPayload
		if err := json.Unmarshal(data, &inv); err != nil {
			logger.Warn("bad inventory payload", zap.String("probe", probeID), zap.Error(err))
			return
		}
		if err := fm.UpdateInventory(probeID, &inv); err != nil {
			logger.Warn("inventory update failed", zap.String("probe", probeID), zap.Error(err))
		} else {
			emitAudit(audit.EventInventoryUpdate, probeID, probeID, "Inventory updated")
		}

	case protocol.MsgCommandResult:
		data, _ := json.Marshal(env.Payload)
		var result protocol.CommandResultPayload
		if err := json.Unmarshal(data, &result); err != nil {
			logger.Warn("bad command result payload", zap.String("probe", probeID), zap.Error(err))
			return
		}
		logger.Info("command result received",
			zap.String("probe", probeID),
			zap.String("request_id", result.RequestID),
			zap.Int("exit_code", result.ExitCode),
		)
		recordAudit(audit.Event{
			Type:    audit.EventCommandResult,
			ProbeID: probeID,
			Actor:   probeID,
			Summary: "Command completed: " + result.RequestID,
			Detail:  map[string]any{"exit_code": result.ExitCode, "duration_ms": result.Duration},
		})
		if err := ct.Complete(result.RequestID, &result); err != nil {
			logger.Debug("no waiting caller for result", zap.String("request_id", result.RequestID))
		}

	case protocol.MsgOutputChunk:
		data, _ := json.Marshal(env.Payload)
		var chunk protocol.OutputChunkPayload
		if err := json.Unmarshal(data, &chunk); err != nil {
			logger.Warn("bad output chunk", zap.String("probe", probeID), zap.Error(err))
			return
		}
		wsHub.DispatchChunk(chunk)
		if chunk.Final {
			logger.Info("streaming command completed",
				zap.String("probe", probeID),
				zap.String("request_id", chunk.RequestID),
				zap.Int("exit_code", chunk.ExitCode),
			)
			// Also complete the cmdtracker so sync callers get notified
			_ = ct.Complete(chunk.RequestID, &protocol.CommandResultPayload{
				RequestID: chunk.RequestID,
				ExitCode:  chunk.ExitCode,
			})
		}

	default:
		logger.Debug("unhandled message type",
			zap.String("probe", probeID),
			zap.String("type", string(env.Type)),
		)
	}
}

func offlineChecker(ctx context.Context, fm fleet.Fleet) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fm.MarkOffline(60 * time.Second)
		}
	}
}

// Template helper functions

func formatLastSeen(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

func templateStatusClass(status string) string {
	switch strings.ToLower(status) {
	case "online":
		return "online"
	case "offline":
		return "offline"
	case "degraded":
		return "degraded"
	default:
		return "pending"
	}
}

func templateHumanizeStatus(status string) string {
	s := strings.ToLower(status)
	if s == "" {
		return "pending"
	}
	return s
}

func humanBytes(v uint64) string {
	if v == 0 {
		return "0 B"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	value := float64(v)
	unit := 0
	for unit < len(units)-1 && value >= 1024 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%.0f %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

func calculateUptime(start time.Time) string {
	if start.IsZero() {
		return "n/a"
	}
	secs := int64(time.Since(start).Seconds())
	if secs < 60 {
		return strconv.FormatInt(secs, 10) + "s"
	}
	mins := secs / 60
	secs %= 60
	hours := mins / 60
	mins %= 60
	days := hours / 24
	hours %= 24

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", secs))
	}
	return strings.Join(parts, " ")
}

type Config struct {
	ListenAddr string
	DataDir    string
}

func loadConfig() (*Config, error) {
	addr := os.Getenv("LEGATOR_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	dataDir := os.Getenv("LEGATOR_DATA_DIR")
	if dataDir == "" {
		dataDir = "/var/lib/legator"
	}
	return &Config{ListenAddr: addr, DataDir: dataDir}, nil
}

// hubConnectedAdapter adapts Hub.Connected() []string to metrics.HubStats.Connected() int.
type hubConnectedAdapter struct {
	hub *cpws.Hub
}

func (a *hubConnectedAdapter) Connected() int {
	return len(a.hub.Connected())
}
