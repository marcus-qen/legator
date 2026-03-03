package providerproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/shared/security"
)

const (
	ScopeProviderProxy = "runner:provider-proxy"
)

var (
	ErrUnauthorized = errors.New("provider proxy unauthorized")
	ErrForbidden    = errors.New("provider proxy forbidden")
)

// TokenValidationRequest binds a run token to the proxy request.
type TokenValidationRequest struct {
	Token     string
	RunID     string
	ProbeID   string
	SessionID string
	Scope     string
	Audience  string
	Consume   bool
}

// TokenClaims contains validated token claims.
type TokenClaims struct {
	RunID     string
	ProbeID   string
	SessionID string
	Issuer    string
	Audience  string
	Scopes    []string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// TokenValidator validates runner tokens for provider proxy calls.
type TokenValidator interface {
	ValidateToken(ctx context.Context, req TokenValidationRequest) (*TokenClaims, error)
}

// TokenValidatorFunc adapts a function to TokenValidator.
type TokenValidatorFunc func(ctx context.Context, req TokenValidationRequest) (*TokenClaims, error)

func (fn TokenValidatorFunc) ValidateToken(ctx context.Context, req TokenValidationRequest) (*TokenClaims, error) {
	return fn(ctx, req)
}

// ProviderCredentials resolve server-side provider connection details.
type ProviderCredentials struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
}

// CredentialResolver returns provider credentials for a proxy request.
type CredentialResolver interface {
	ResolveProviderCredentials(ctx context.Context, runID, requestedModel string) (ProviderCredentials, error)
}

// CredentialResolverFunc adapts a function to CredentialResolver.
type CredentialResolverFunc func(ctx context.Context, runID, requestedModel string) (ProviderCredentials, error)

func (fn CredentialResolverFunc) ResolveProviderCredentials(ctx context.Context, runID, requestedModel string) (ProviderCredentials, error) {
	return fn(ctx, runID, requestedModel)
}

// AuditEvent records proxy usage in the control-plane audit log.
type AuditEvent struct {
	RunID         string
	Actor         string
	SessionID     string
	Model         string
	InputTokens   int
	OutputTokens  int
	TotalTokens   int
	EstimatedCost float64
}

// AuditSink receives proxy audit events.
type AuditSink interface {
	RecordProviderProxyEvent(ctx context.Context, evt AuditEvent)
}

// AuditSinkFunc adapts a function to AuditSink.
type AuditSinkFunc func(ctx context.Context, evt AuditEvent)

func (fn AuditSinkFunc) RecordProviderProxyEvent(ctx context.Context, evt AuditEvent) {
	fn(ctx, evt)
}

// ProxyRequest is the API payload accepted by the provider proxy endpoint.
type ProxyRequest struct {
	RunToken    string    `json:"run_token"`
	SessionID   string    `json:"session_id"`
	Model       string    `json:"model,omitempty"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
}

// Message is an OpenAI-compatible message object.
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
	Name    string `json:"name,omitempty"`
}

// ProxyConfig wires token validation, credential resolution, and spend tracking.
type ProxyConfig struct {
	TokenValidator   TokenValidator
	Credentials      CredentialResolver
	SpendStore       *SpendStore
	AuditSink        AuditSink
	HTTPClient       *http.Client
	MaxTokensPerRun  int
	MaxCostPerRun    float64
	UpstreamEndpoint string
}

// Proxy forwards OpenAI-compatible requests through control-plane-managed credentials.
type Proxy struct {
	tokens            TokenValidator
	credentials       CredentialResolver
	spend             *SpendStore
	audit             AuditSink
	httpClient        *http.Client
	maxTokensPerRun   int
	maxCostPerRun     float64
	upstreamChatRoute string
}

func New(cfg ProxyConfig) (*Proxy, error) {
	if cfg.TokenValidator == nil {
		return nil, fmt.Errorf("token validator is required")
	}
	if cfg.Credentials == nil {
		return nil, fmt.Errorf("credential resolver is required")
	}
	if cfg.SpendStore == nil {
		return nil, fmt.Errorf("spend store is required")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	route := strings.TrimSpace(cfg.UpstreamEndpoint)
	if route == "" {
		route = "/chat/completions"
	}
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}

	return &Proxy{
		tokens:            cfg.TokenValidator,
		credentials:       cfg.Credentials,
		spend:             cfg.SpendStore,
		audit:             cfg.AuditSink,
		httpClient:        client,
		maxTokensPerRun:   cfg.MaxTokensPerRun,
		maxCostPerRun:     cfg.MaxCostPerRun,
		upstreamChatRoute: route,
	}, nil
}

// HandleHTTP serves POST /api/v1/runs/{id}/provider-proxy.
func (p *Proxy) HandleHTTP(w http.ResponseWriter, r *http.Request, runID string) {
	if p == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "provider proxy unavailable")
		return
	}
	if r == nil || r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	runID = strings.TrimSpace(runID)
	if runID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "run id required")
		return
	}

	var req ProxyRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}

	resp, statusErr := p.execute(r.Context(), runID, req)
	if statusErr != nil {
		writeError(w, statusErr.Status, statusErr.Code, statusErr.Message)
		return
	}

	contentType := strings.TrimSpace(resp.ContentType)
	if contentType == "" {
		contentType = "application/json"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(resp.Body)
}

type executeResult struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

type statusError struct {
	Status  int
	Code    string
	Message string
}

func (p *Proxy) execute(ctx context.Context, runID string, req ProxyRequest) (*executeResult, *statusError) {
	runToken := strings.TrimSpace(req.RunToken)
	if runToken == "" {
		return nil, &statusError{Status: http.StatusUnauthorized, Code: "invalid_run_token", Message: "run_token is required"}
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return nil, &statusError{Status: http.StatusBadRequest, Code: "invalid_request", Message: "session_id is required"}
	}
	if len(req.Messages) == 0 {
		return nil, &statusError{Status: http.StatusBadRequest, Code: "invalid_request", Message: "messages are required"}
	}

	claims, err := p.tokens.ValidateToken(ctx, TokenValidationRequest{
		Token:     runToken,
		RunID:     runID,
		SessionID: sessionID,
		Scope:     ScopeProviderProxy,
		Audience:  ScopeProviderProxy,
		Consume:   true,
	})
	if err != nil {
		return nil, mapTokenError(err)
	}

	totals, err := p.spend.Totals(ctx, runID)
	if err != nil {
		return nil, &statusError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "failed to query spend"}
	}
	if p.maxTokensPerRun > 0 {
		if totals.TotalTokens >= p.maxTokensPerRun {
			return nil, &statusError{Status: http.StatusTooManyRequests, Code: "spend_limit_exceeded", Message: "max tokens per run exceeded"}
		}
		if req.MaxTokens != nil && *req.MaxTokens > 0 && (totals.TotalTokens+*req.MaxTokens) > p.maxTokensPerRun {
			return nil, &statusError{Status: http.StatusTooManyRequests, Code: "spend_limit_exceeded", Message: "max tokens per run exceeded"}
		}
	}
	if p.maxCostPerRun > 0 && totals.EstimatedCost >= p.maxCostPerRun {
		return nil, &statusError{Status: http.StatusTooManyRequests, Code: "spend_limit_exceeded", Message: "max cost per run exceeded"}
	}

	creds, err := p.credentials.ResolveProviderCredentials(ctx, runID, strings.TrimSpace(req.Model))
	if err != nil {
		return nil, &statusError{Status: http.StatusServiceUnavailable, Code: "provider_unavailable", Message: "provider credentials unavailable"}
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(creds.Model)
	}
	if model == "" {
		return nil, &statusError{Status: http.StatusBadRequest, Code: "invalid_request", Message: "model is required"}
	}
	baseURL := strings.TrimSpace(creds.BaseURL)
	if baseURL == "" {
		return nil, &statusError{Status: http.StatusServiceUnavailable, Code: "provider_unavailable", Message: "provider base URL unavailable"}
	}

	upstreamReq := struct {
		Model       string    `json:"model"`
		Messages    []Message `json:"messages"`
		Temperature *float64  `json:"temperature,omitempty"`
		MaxTokens   *int      `json:"max_tokens,omitempty"`
	}{
		Model:       model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	body, err := json.Marshal(upstreamReq)
	if err != nil {
		return nil, &statusError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "failed to encode request"}
	}

	url := strings.TrimSuffix(baseURL, "/") + p.upstreamChatRoute
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, &statusError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "failed to create provider request"}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(creds.APIKey); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}

	providerResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, &statusError{Status: http.StatusBadGateway, Code: "provider_request_failed", Message: "provider request failed"}
	}
	defer providerResp.Body.Close()

	respBody, err := io.ReadAll(providerResp.Body)
	if err != nil {
		return nil, &statusError{Status: http.StatusBadGateway, Code: "provider_request_failed", Message: "failed to read provider response"}
	}

	if providerResp.StatusCode < 200 || providerResp.StatusCode > 299 {
		sanitized := sanitizeProviderError(respBody, creds.APIKey)
		return nil, &statusError{Status: http.StatusBadGateway, Code: "provider_error", Message: sanitized}
	}

	usage, modelFromResp := extractUsage(respBody)
	if modelFromResp != "" {
		model = modelFromResp
	}
	cost := EstimateCostUSD(model, usage.InputTokens, usage.OutputTokens)

	totals, err = p.spend.Record(ctx, SpendRecord{
		RunID:         runID,
		JobID:         strings.TrimSpace(claims.RunID),
		SessionID:     sessionID,
		Model:         model,
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		TotalTokens:   usage.TotalTokens,
		EstimatedCost: cost,
	})
	if err != nil {
		return nil, &statusError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "failed to record spend"}
	}

	if p.maxTokensPerRun > 0 && totals.TotalTokens > p.maxTokensPerRun {
		return nil, &statusError{Status: http.StatusTooManyRequests, Code: "spend_limit_exceeded", Message: "max tokens per run exceeded"}
	}
	if p.maxCostPerRun > 0 && totals.EstimatedCost > p.maxCostPerRun {
		return nil, &statusError{Status: http.StatusTooManyRequests, Code: "spend_limit_exceeded", Message: "max cost per run exceeded"}
	}

	if p.audit != nil {
		actor := strings.TrimSpace(claims.Issuer)
		if actor == "" {
			actor = strings.TrimSpace(claims.SessionID)
		}
		if actor == "" {
			actor = "runner"
		}
		p.audit.RecordProviderProxyEvent(ctx, AuditEvent{
			RunID:         runID,
			Actor:         actor,
			SessionID:     sessionID,
			Model:         model,
			InputTokens:   usage.InputTokens,
			OutputTokens:  usage.OutputTokens,
			TotalTokens:   usage.TotalTokens,
			EstimatedCost: cost,
		})
	}

	return &executeResult{
		StatusCode:  providerResp.StatusCode,
		ContentType: providerResp.Header.Get("Content-Type"),
		Body:        respBody,
	}, nil
}

func mapTokenError(err error) *statusError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrForbidden):
		return &statusError{Status: http.StatusForbidden, Code: "run_token_scope_rejected", Message: "run token scope rejected"}
	default:
		return &statusError{Status: http.StatusUnauthorized, Code: "invalid_run_token", Message: "invalid run token"}
	}
}

func sanitizeProviderError(body []byte, apiKey string) string {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = "provider request failed"
	}
	if key := strings.TrimSpace(apiKey); key != "" {
		msg = strings.ReplaceAll(msg, key, "[REDACTED]")
	}
	msg = security.Sanitize(msg)
	if len(msg) > 2048 {
		msg = msg[:2048] + "..."
	}
	return msg
}

type usageEnvelope struct {
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

func extractUsage(body []byte) (usage, string) {
	var parsed usageEnvelope
	if err := json.Unmarshal(body, &parsed); err != nil {
		return usage{}, ""
	}
	u := usage{
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
		TotalTokens:  parsed.Usage.TotalTokens,
	}
	if u.TotalTokens <= 0 {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	return u, strings.TrimSpace(parsed.Model)
}

func EstimateCostUSD(model string, inputTokens, outputTokens int) float64 {
	model = strings.ToLower(strings.TrimSpace(model))
	inRate, outRate := 0.50, 1.50 // USD per 1M tokens default estimate
	switch {
	case strings.Contains(model, "gpt-4o-mini"):
		inRate, outRate = 0.15, 0.60
	case strings.Contains(model, "gpt-4o"):
		inRate, outRate = 2.50, 10.00
	case strings.Contains(model, "gpt-4.1-mini"):
		inRate, outRate = 0.40, 1.60
	case strings.Contains(model, "gpt-4.1"):
		inRate, outRate = 2.00, 8.00
	}
	return (float64(inputTokens)/1_000_000.0)*inRate + (float64(outputTokens)/1_000_000.0)*outRate
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if strings.TrimSpace(code) == "" {
		code = "error"
	}
	if strings.TrimSpace(message) == "" {
		message = http.StatusText(status)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   code,
		"message": message,
	})
}
