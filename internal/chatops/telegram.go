package chatops

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

const (
	defaultAPIRequestTimeout = 10 * time.Second
	defaultConfirmationTTL   = 2 * time.Minute
)

// UserBinding maps a Telegram chat to an API identity used for authorization checks.
type UserBinding struct {
	ChatID  int64             `json:"chatId"`
	Subject string            `json:"subject,omitempty"`
	Email   string            `json:"email,omitempty"`
	Name    string            `json:"name,omitempty"`
	Groups  []string          `json:"groups,omitempty"`
	Claims  map[string]string `json:"claims,omitempty"`
}

// TelegramConfig controls the Telegram-first ChatOps MVP bot.
type TelegramConfig struct {
	BotToken string

	// APIBaseURL points at the Legator API endpoint used for all command execution.
	// Example: http://127.0.0.1:8090
	APIBaseURL string

	// APIIssuer and APIAudience are copied into generated JWT claims.
	APIIssuer   string
	APIAudience string

	PollInterval    time.Duration
	LongPollTimeout time.Duration
	ConfirmationTTL time.Duration

	UserBindings []UserBinding
	HTTPClient   *http.Client
}

// TelegramBot polls Telegram updates and routes commands through Legator API gates.
type TelegramBot struct {
	cfg    TelegramConfig
	log    logr.Logger
	client *http.Client

	bindings map[int64]UserBinding
	offset   int64

	confirmMu     sync.Mutex
	confirmations map[string]pendingConfirmation

	sendMessageFn func(context.Context, int64, string) error
}

type pendingConfirmation struct {
	ApprovalID string
	Decision   string
	Reason     string
	Code       string
	Tier       string
	Target     string
	ExpiresAt  time.Time
}

// ParseBindingsJSON decodes CHATOPS_TELEGRAM_BINDINGS style JSON.
func ParseBindingsJSON(raw string) ([]UserBinding, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var bindings []UserBinding
	if err := json.Unmarshal([]byte(raw), &bindings); err != nil {
		return nil, fmt.Errorf("parse telegram bindings json: %w", err)
	}
	for i := range bindings {
		if bindings[i].ChatID == 0 {
			return nil, fmt.Errorf("telegram binding index %d has empty chatId", i)
		}
		if bindings[i].Subject == "" {
			bindings[i].Subject = fmt.Sprintf("telegram:%d", bindings[i].ChatID)
		}
		if bindings[i].Email == "" {
			bindings[i].Email = fmt.Sprintf("telegram-%d@chatops.local", bindings[i].ChatID)
		}
		if bindings[i].Name == "" {
			bindings[i].Name = fmt.Sprintf("Telegram User %d", bindings[i].ChatID)
		}
	}

	return bindings, nil
}

// NewTelegramBot creates a ChatOps Telegram bot runnable.
func NewTelegramBot(cfg TelegramConfig, log logr.Logger) (*TelegramBot, error) {
	if cfg.BotToken == "" {
		return nil, errors.New("telegram bot token is required")
	}
	if cfg.APIBaseURL == "" {
		return nil, errors.New("chatops api base url is required")
	}
	if _, err := url.ParseRequestURI(cfg.APIBaseURL); err != nil {
		return nil, fmt.Errorf("invalid chatops api base url: %w", err)
	}
	if len(cfg.UserBindings) == 0 {
		return nil, errors.New("at least one telegram user binding is required")
	}

	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.LongPollTimeout <= 0 {
		cfg.LongPollTimeout = 25 * time.Second
	}
	if cfg.ConfirmationTTL <= 0 {
		cfg.ConfirmationTTL = defaultConfirmationTTL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}

	bindings := make(map[int64]UserBinding, len(cfg.UserBindings))
	for _, binding := range cfg.UserBindings {
		if binding.ChatID == 0 {
			return nil, errors.New("telegram binding contains empty chat id")
		}
		if binding.Subject == "" {
			binding.Subject = fmt.Sprintf("telegram:%d", binding.ChatID)
		}
		if binding.Email == "" {
			binding.Email = fmt.Sprintf("telegram-%d@chatops.local", binding.ChatID)
		}
		if binding.Name == "" {
			binding.Name = fmt.Sprintf("Telegram User %d", binding.ChatID)
		}
		bindings[binding.ChatID] = binding
	}

	bot := &TelegramBot{
		cfg:           cfg,
		log:           log.WithName("chatops-telegram"),
		client:        cfg.HTTPClient,
		bindings:      bindings,
		confirmations: make(map[string]pendingConfirmation),
	}
	bot.sendMessageFn = bot.sendMessage
	return bot, nil
}

// NeedLeaderElection ensures only one replica polls Telegram updates.
func (b *TelegramBot) NeedLeaderElection() bool {
	return true
}

// Start runs the long-poll loop until context cancellation.
func (b *TelegramBot) Start(ctx context.Context) error {
	b.log.Info("Telegram ChatOps bot starting", "bindings", len(b.bindings), "apiBaseURL", b.cfg.APIBaseURL)

	for {
		if err := b.pollOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			b.log.Error(err, "Telegram poll failed")
		}

		select {
		case <-ctx.Done():
			b.log.Info("Telegram ChatOps bot stopping")
			return nil
		case <-time.After(b.cfg.PollInterval):
		}
	}
}

func (b *TelegramBot) pollOnce(ctx context.Context) error {
	values := url.Values{}
	values.Set("timeout", strconv.Itoa(int(b.cfg.LongPollTimeout.Seconds())))
	if b.offset > 0 {
		values.Set("offset", strconv.FormatInt(b.offset, 10))
	}

	endpoint := b.telegramEndpoint("getUpdates") + "?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build getUpdates request: %w", err)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram getUpdates: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read getUpdates response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram getUpdates returned %d: %s", resp.StatusCode, string(body))
	}

	var payload telegramResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("decode getUpdates response: %w", err)
	}
	if !payload.OK {
		return fmt.Errorf("telegram getUpdates api error: %s", payload.Description)
	}

	for _, upd := range payload.Result {
		if upd.UpdateID >= b.offset {
			b.offset = upd.UpdateID + 1
		}
		if upd.Message == nil {
			continue
		}
		if err := b.handleIncomingMessage(ctx, *upd.Message); err != nil {
			b.log.Error(err, "Failed handling Telegram message", "chatID", upd.Message.Chat.ID)
		}
	}

	return nil
}

func (b *TelegramBot) handleIncomingMessage(ctx context.Context, msg telegramMessage) error {
	text := strings.TrimSpace(msg.Text)
	if text == "" || !strings.HasPrefix(text, "/") {
		return nil
	}

	binding, ok := b.bindings[msg.Chat.ID]
	if !ok {
		return b.sendMessageFn(ctx, msg.Chat.ID, "This chat is not authorized for Legator ChatOps.")
	}

	response := b.processCommand(ctx, binding, msg.Chat.ID, text)
	if response == "" {
		return nil
	}

	return b.sendMessageFn(ctx, msg.Chat.ID, response)
}

func (b *TelegramBot) processCommand(ctx context.Context, binding UserBinding, chatID int64, text string) string {
	if err := b.checkChatPermission(ctx, binding); err != nil {
		return "Access denied: " + err.Error()
	}

	cmd, args := parseCommand(text)
	switch cmd {
	case "help", "start":
		return "Legator ChatOps commands:\n" +
			"/status — summary of agents and recent runs\n" +
			"/inventory [limit] — show managed inventory\n" +
			"/run <id> — lookup a run\n" +
			"/approvals — list pending approvals\n" +
			"/approve <id> [reason] — start approval confirmation flow\n" +
			"/deny <id> [reason] — start deny confirmation flow\n" +
			"/confirm <id> <code> — complete a pending approval/deny"
	case "status":
		msg, err := b.statusCommand(ctx, binding)
		if err != nil {
			return "Status failed: " + err.Error()
		}
		return msg
	case "inventory":
		limit := 5
		if len(args) > 0 {
			if n, err := strconv.Atoi(args[0]); err == nil && n > 0 {
				if n > 15 {
					n = 15
				}
				limit = n
			}
		}
		msg, err := b.inventoryCommand(ctx, binding, limit)
		if err != nil {
			return "Inventory lookup failed: " + err.Error()
		}
		return msg
	case "run":
		if len(args) < 1 {
			return "Usage: /run <run-id>"
		}
		msg, err := b.runLookupCommand(ctx, binding, args[0])
		if err != nil {
			return "Run lookup failed: " + err.Error()
		}
		return msg
	case "approvals":
		msg, err := b.approvalsCommand(ctx, binding)
		if err != nil {
			return "Approvals lookup failed: " + err.Error()
		}
		return msg
	case "approve", "deny":
		if len(args) < 1 {
			return fmt.Sprintf("Usage: /%s <approval-id> [reason]", cmd)
		}
		reason := strings.TrimSpace(strings.Join(args[1:], " "))
		if reason == "" {
			reason = fmt.Sprintf("via telegram chatops by %s", binding.Email)
		}
		msg, err := b.startApprovalConfirmation(ctx, binding, chatID, cmd, args[0], reason)
		if err != nil {
			return fmt.Sprintf("/%s failed: %v", cmd, err)
		}
		return msg
	case "confirm":
		if len(args) < 2 {
			return "Usage: /confirm <approval-id> <code>"
		}
		msg, err := b.completeApprovalConfirmation(ctx, binding, chatID, args[0], args[1])
		if err != nil {
			return fmt.Sprintf("/confirm failed: %v", err)
		}
		return msg
	default:
		return "Unknown command. Use /help."
	}
}

func (b *TelegramBot) checkChatPermission(ctx context.Context, binding UserBinding) error {
	var resp struct {
		Permissions map[string]struct {
			Allowed bool   `json:"allowed"`
			Reason  string `json:"reason"`
		} `json:"permissions"`
	}

	status, body, err := b.apiRequest(ctx, binding, http.MethodGet, "/api/v1/me", nil, &resp)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return errors.New(apiErrorMessage(status, body))
	}

	if perm, ok := resp.Permissions["chat:use"]; ok && !perm.Allowed {
		if perm.Reason != "" {
			return errors.New(perm.Reason)
		}
		return errors.New("chat permission denied by policy")
	}

	return nil
}

func (b *TelegramBot) statusCommand(ctx context.Context, binding UserBinding) (string, error) {
	var agents struct {
		Total  int `json:"total"`
		Agents []struct {
			Phase string `json:"phase"`
		} `json:"agents"`
	}
	status, body, err := b.apiRequest(ctx, binding, http.MethodGet, "/api/v1/agents", nil, &agents)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", errors.New(apiErrorMessage(status, body))
	}

	var runs struct {
		Runs []struct {
			Name  string `json:"name"`
			Agent string `json:"agent"`
			Phase string `json:"phase"`
		} `json:"runs"`
	}
	status, body, err = b.apiRequest(ctx, binding, http.MethodGet, "/api/v1/runs", nil, &runs)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", errors.New(apiErrorMessage(status, body))
	}

	ready := 0
	for _, item := range agents.Agents {
		if strings.EqualFold(item.Phase, "Ready") {
			ready++
		}
	}

	lines := []string{
		"Legator status",
		fmt.Sprintf("Agents: %d total · %d ready", agents.Total, ready),
	}

	if len(runs.Runs) == 0 {
		lines = append(lines, "Recent runs: none")
	} else {
		lines = append(lines, "Recent runs:")
		for i, run := range runs.Runs {
			if i >= 3 {
				break
			}
			lines = append(lines, fmt.Sprintf("- %s · %s · %s", run.Name, run.Agent, run.Phase))
		}
	}

	return strings.Join(lines, "\n"), nil
}

func (b *TelegramBot) inventoryCommand(ctx context.Context, binding UserBinding, limit int) (string, error) {
	var inv struct {
		Total   int              `json:"total"`
		Source  string           `json:"source"`
		Devices []map[string]any `json:"devices"`
	}

	status, body, err := b.apiRequest(ctx, binding, http.MethodGet, "/api/v1/inventory", nil, &inv)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", errors.New(apiErrorMessage(status, body))
	}

	lines := []string{fmt.Sprintf("Inventory: %d devices (source: %s)", inv.Total, inv.Source)}
	if len(inv.Devices) == 0 {
		return strings.Join(lines, "\n"), nil
	}

	if limit > len(inv.Devices) {
		limit = len(inv.Devices)
	}
	for i := 0; i < limit; i++ {
		d := inv.Devices[i]
		name := firstString(d, "name", "hostname", "id")
		if name == "" {
			name = fmt.Sprintf("device-%d", i+1)
		}
		urlOrIP := firstString(d, "url", "ip", "address")
		if urlOrIP != "" {
			lines = append(lines, fmt.Sprintf("- %s (%s)", name, urlOrIP))
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s", name))
	}

	return strings.Join(lines, "\n"), nil
}

func (b *TelegramBot) runLookupCommand(ctx context.Context, binding UserBinding, id string) (string, error) {
	status, body, err := b.apiRequest(ctx, binding, http.MethodGet, "/api/v1/runs/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", errors.New(apiErrorMessage(status, body))
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode run response: %w", err)
	}

	name := firstString(payload, "name")
	agent := nestedString(payload, "spec", "agentRef")
	phase := nestedString(payload, "status", "phase")
	trigger := nestedString(payload, "spec", "trigger")

	lines := []string{fmt.Sprintf("Run %s", name)}
	if agent != "" {
		lines = append(lines, "Agent: "+agent)
	}
	if phase != "" {
		lines = append(lines, "Phase: "+phase)
	}
	if trigger != "" {
		lines = append(lines, "Trigger: "+trigger)
	}
	return strings.Join(lines, "\n"), nil
}

func (b *TelegramBot) approvalsCommand(ctx context.Context, binding UserBinding) (string, error) {
	var payload struct {
		Approvals []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				AgentName string `json:"agentName"`
				Action    struct {
					Tool   string `json:"tool"`
					Target string `json:"target"`
					Tier   string `json:"tier"`
				} `json:"action"`
			} `json:"spec"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"approvals"`
	}

	status, body, err := b.apiRequest(ctx, binding, http.MethodGet, "/api/v1/approvals", nil, &payload)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", errors.New(apiErrorMessage(status, body))
	}

	pending := make([]string, 0)
	for _, item := range payload.Approvals {
		phase := strings.TrimSpace(item.Status.Phase)
		if phase != "" && !strings.EqualFold(phase, "Pending") {
			continue
		}
		pending = append(pending, fmt.Sprintf("- %s · %s · %s", item.Metadata.Name, item.Spec.Action.Tool, item.Spec.Action.Target))
	}

	if len(pending) == 0 {
		return "No pending approval requests.", nil
	}

	if len(pending) > 10 {
		pending = pending[:10]
	}

	return "Pending approvals:\n" + strings.Join(pending, "\n"), nil
}

type approvalSummary struct {
	Name   string
	Phase  string
	Tier   string
	Tool   string
	Target string
}

func (b *TelegramBot) startApprovalConfirmation(ctx context.Context, binding UserBinding, chatID int64, decision, id, reason string) (string, error) {
	approval, err := b.lookupApproval(ctx, binding, id)
	if err != nil {
		return "", err
	}
	if approval == nil {
		return "", fmt.Errorf("approval %s not found", id)
	}
	if !strings.EqualFold(approval.Phase, "Pending") {
		return "", fmt.Errorf("approval %s is not pending (phase: %s)", id, approval.Phase)
	}

	code, err := generateConfirmationCode()
	if err != nil {
		return "", err
	}

	expiresAt := time.Now().Add(b.cfg.ConfirmationTTL)
	entry := pendingConfirmation{
		ApprovalID: id,
		Decision:   decision,
		Reason:     reason,
		Code:       code,
		Tier:       approval.Tier,
		Target:     approval.Target,
		ExpiresAt:  expiresAt,
	}

	b.confirmMu.Lock()
	b.pruneExpiredConfirmationsLocked(time.Now())
	b.confirmations[b.confirmationKey(chatID, id)] = entry
	b.confirmMu.Unlock()

	return fmt.Sprintf(
		"Typed confirmation required for /%s %s (tier: %s, target: %s).\nReply within %s with:\n/confirm %s %s",
		decision,
		id,
		safeDefault(approval.Tier, "unknown"),
		safeDefault(approval.Target, "unknown"),
		b.cfg.ConfirmationTTL.Round(time.Second).String(),
		id,
		code,
	), nil
}

func (b *TelegramBot) completeApprovalConfirmation(ctx context.Context, binding UserBinding, chatID int64, id, code string) (string, error) {
	key := b.confirmationKey(chatID, id)
	now := time.Now()

	b.confirmMu.Lock()
	entry, ok := b.confirmations[key]
	if !ok {
		b.confirmMu.Unlock()
		return "", fmt.Errorf("no pending confirmation for %s", id)
	}
	if now.After(entry.ExpiresAt) {
		delete(b.confirmations, key)
		b.confirmMu.Unlock()
		return "", fmt.Errorf("confirmation expired for %s; run /%s again", id, entry.Decision)
	}
	if !strings.EqualFold(strings.TrimSpace(code), entry.Code) {
		b.confirmMu.Unlock()
		return "", errors.New("confirmation code mismatch")
	}
	b.confirmMu.Unlock()

	msg, err := b.approvalDecisionCommand(ctx, binding, entry.Decision, id, entry.Reason)
	if err != nil {
		return "", err
	}

	b.confirmMu.Lock()
	delete(b.confirmations, key)
	b.confirmMu.Unlock()

	return msg + " (typed confirmation accepted)", nil
}

func (b *TelegramBot) lookupApproval(ctx context.Context, binding UserBinding, id string) (*approvalSummary, error) {
	var payload struct {
		Approvals []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Action struct {
					Tier   string `json:"tier"`
					Tool   string `json:"tool"`
					Target string `json:"target"`
				} `json:"action"`
			} `json:"spec"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"approvals"`
	}

	status, body, err := b.apiRequest(ctx, binding, http.MethodGet, "/api/v1/approvals", nil, &payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, errors.New(apiErrorMessage(status, body))
	}

	for _, item := range payload.Approvals {
		if item.Metadata.Name != id {
			continue
		}
		return &approvalSummary{
			Name:   item.Metadata.Name,
			Phase:  item.Status.Phase,
			Tier:   item.Spec.Action.Tier,
			Tool:   item.Spec.Action.Tool,
			Target: item.Spec.Action.Target,
		}, nil
	}
	return nil, nil
}

func (b *TelegramBot) confirmationKey(chatID int64, approvalID string) string {
	return fmt.Sprintf("%d:%s", chatID, approvalID)
}

func (b *TelegramBot) pruneExpiredConfirmationsLocked(now time.Time) {
	for key, value := range b.confirmations {
		if now.After(value.ExpiresAt) {
			delete(b.confirmations, key)
		}
	}
}

func generateConfirmationCode() (string, error) {
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate confirmation code: %w", err)
	}
	return strings.ToUpper(hex.EncodeToString(buf)), nil
}

func safeDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func (b *TelegramBot) approvalDecisionCommand(ctx context.Context, binding UserBinding, decision, id, reason string) (string, error) {
	payload := map[string]string{
		"decision": decision,
		"reason":   reason,
	}
	status, body, err := b.apiRequest(ctx, binding, http.MethodPost, "/api/v1/approvals/"+url.PathEscape(id), payload, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", errors.New(apiErrorMessage(status, body))
	}

	verb := "approved"
	if decision == "deny" {
		verb = "denied"
	}
	return fmt.Sprintf("Approval %s %s.", id, verb), nil
}

func (b *TelegramBot) apiRequest(
	ctx context.Context,
	binding UserBinding,
	method,
	path string,
	requestBody any,
	responseBody any,
) (int, []byte, error) {
	fullURL := strings.TrimRight(b.cfg.APIBaseURL, "/") + path

	var body io.Reader
	if requestBody != nil {
		payload, err := json.Marshal(requestBody)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal api request body: %w", err)
		}
		body = bytes.NewReader(payload)
	}

	reqCtx, cancel := context.WithTimeout(ctx, defaultAPIRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, fullURL, body)
	if err != nil {
		return 0, nil, fmt.Errorf("build api request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+b.makeJWT(binding))
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("api request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read api response: %w", err)
	}

	if responseBody != nil && len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, responseBody); err != nil {
			return resp.StatusCode, respBytes, fmt.Errorf("decode api response: %w", err)
		}
	}

	return resp.StatusCode, respBytes, nil
}

func (b *TelegramBot) makeJWT(binding UserBinding) string {
	claims := map[string]any{
		"sub":   binding.Subject,
		"email": binding.Email,
		"name":  binding.Name,
		"exp":   time.Now().Add(5 * time.Minute).Unix(),
		"iat":   time.Now().Unix(),
	}
	if len(binding.Groups) > 0 {
		claims["groups"] = binding.Groups
	}
	for key, value := range binding.Claims {
		claims[key] = value
	}
	if b.cfg.APIIssuer != "" {
		claims["iss"] = b.cfg.APIIssuer
	}
	if b.cfg.APIAudience != "" {
		claims["aud"] = b.cfg.APIAudience
	}

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return fmt.Sprintf("%s.%s.chatops", header, body)
}

func (b *TelegramBot) sendMessage(ctx context.Context, chatID int64, text string) error {
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	raw, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.telegramEndpoint("sendMessage"), bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build sendMessage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("send telegram message: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read sendMessage response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram sendMessage returned %d: %s", resp.StatusCode, string(body))
	}

	var payloadResp telegramResponse
	if err := json.Unmarshal(body, &payloadResp); err != nil {
		return fmt.Errorf("decode sendMessage response: %w", err)
	}
	if !payloadResp.OK {
		return fmt.Errorf("telegram sendMessage api error: %s", payloadResp.Description)
	}

	return nil
}

func (b *TelegramBot) telegramEndpoint(method string) string {
	return fmt.Sprintf("https://api.telegram.org/bot%s/%s", b.cfg.BotToken, method)
}

func parseCommand(text string) (string, []string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", nil
	}
	cmd := strings.TrimPrefix(fields[0], "/")
	if idx := strings.Index(cmd, "@"); idx > 0 {
		cmd = cmd[:idx]
	}
	cmd = strings.ToLower(cmd)
	if len(fields) == 1 {
		return cmd, nil
	}
	return cmd, fields[1:]
}

func apiErrorMessage(status int, body []byte) string {
	var parsed struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error != "" {
		return fmt.Sprintf("api %d: %s", status, parsed.Error)
	}
	if len(body) == 0 {
		return fmt.Sprintf("api %d", status)
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 240 {
		msg = msg[:237] + "..."
	}
	return fmt.Sprintf("api %d: %s", status, msg)
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			if s, ok := value.(string); ok {
				return s
			}
		}
	}
	return ""
}

func nestedString(m map[string]any, path ...string) string {
	if len(path) == 0 {
		return ""
	}
	current := any(m)
	for _, key := range path {
		obj, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		next, ok := obj[key]
		if !ok {
			return ""
		}
		current = next
	}
	s, _ := current.(string)
	return s
}

type telegramResponse struct {
	OK          bool             `json:"ok"`
	Description string           `json:"description,omitempty"`
	Result      []telegramUpdate `json:"result,omitempty"`
}

type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message,omitempty"`
}

type telegramMessage struct {
	MessageID int64        `json:"message_id"`
	Text      string       `json:"text"`
	Chat      telegramChat `json:"chat"`
}

type telegramChat struct {
	ID int64 `json:"id"`
}
