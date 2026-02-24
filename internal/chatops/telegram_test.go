package chatops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

func TestHandleIncomingMessageRejectsUnknownChatWithoutAPICall(t *testing.T) {
	t.Parallel()

	var apiCalls int
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer apiSrv.Close()

	bot := mustNewTestBot(t, TelegramConfig{
		BotToken:   "test-token",
		APIBaseURL: apiSrv.URL,
		UserBindings: []UserBinding{
			{ChatID: 1234, Email: "operator@example.com", Subject: "telegram:1234", Groups: []string{"legator-operator"}},
		},
	})

	var sent []string
	bot.sendMessageFn = func(_ context.Context, _ int64, text string) error {
		sent = append(sent, text)
		return nil
	}

	err := bot.handleIncomingMessage(context.Background(), telegramMessage{
		Text: "/status",
		Chat: telegramChat{ID: 9999},
	})
	if err != nil {
		t.Fatalf("handleIncomingMessage returned error: %v", err)
	}

	if apiCalls != 0 {
		t.Fatalf("apiCalls = %d, want 0", apiCalls)
	}
	if len(sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(sent))
	}
	if !strings.Contains(sent[0], "not authorized") {
		t.Fatalf("unexpected response: %q", sent[0])
	}
}

func TestProcessCommandDeniesWhenChatPermissionMissing(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		apiCalls []string
	)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		apiCalls = append(apiCalls, r.URL.Path)
		mu.Unlock()

		switch r.URL.Path {
		case "/api/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"permissions": map[string]any{
					"chat:use": map[string]any{
						"allowed": false,
						"reason":  "role viewer does not permit action chat:use",
					},
				},
			})
		default:
			http.Error(w, `{"error":"unexpected endpoint"}`, http.StatusInternalServerError)
		}
	}))
	defer apiSrv.Close()

	bot := mustNewTestBot(t, TelegramConfig{
		BotToken:   "test-token",
		APIBaseURL: apiSrv.URL,
		UserBindings: []UserBinding{
			{ChatID: 1234, Email: "viewer@example.com", Subject: "telegram:1234", Groups: []string{"legator-viewer"}},
		},
	})

	var sent []string
	bot.sendMessageFn = func(_ context.Context, _ int64, text string) error {
		sent = append(sent, text)
		return nil
	}

	err := bot.handleIncomingMessage(context.Background(), telegramMessage{
		Text: "/status",
		Chat: telegramChat{ID: 1234},
	})
	if err != nil {
		t.Fatalf("handleIncomingMessage returned error: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(sent))
	}
	if !strings.Contains(sent[0], "Access denied") {
		t.Fatalf("expected access denied response, got %q", sent[0])
	}
	if !strings.Contains(sent[0], "chat:use") {
		t.Fatalf("expected chat permission reason, got %q", sent[0])
	}

	mu.Lock()
	defer mu.Unlock()
	if len(apiCalls) != 1 || apiCalls[0] != "/api/v1/me" {
		t.Fatalf("api calls = %#v, want only /api/v1/me", apiCalls)
	}
}

func TestApproveStartsTypedConfirmationWithoutMutatingAPI(t *testing.T) {
	t.Parallel()

	var approvalCalls int
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"permissions": map[string]any{"chat:use": map[string]any{"allowed": true}}})
		case r.URL.Path == "/api/v1/approvals" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"approvals": []map[string]any{
					{
						"metadata": map[string]any{"name": "req-1"},
						"spec":     map[string]any{"action": map[string]any{"tier": "destructive", "tool": "ssh.exec", "target": "castra"}},
						"status":   map[string]any{"phase": "Pending"},
					},
				},
			})
		case strings.HasPrefix(r.URL.Path, "/api/v1/approvals/") && r.Method == http.MethodPost:
			approvalCalls++
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, `{"error":"unexpected endpoint"}`, http.StatusInternalServerError)
		}
	}))
	defer apiSrv.Close()

	bot := mustNewTestBot(t, TelegramConfig{
		BotToken:   "test-token",
		APIBaseURL: apiSrv.URL,
		UserBindings: []UserBinding{
			{ChatID: 1234, Email: "operator@example.com", Subject: "telegram:1234", Groups: []string{"legator-operator"}},
		},
	})

	var sent []string
	bot.sendMessageFn = func(_ context.Context, _ int64, text string) error {
		sent = append(sent, text)
		return nil
	}

	err := bot.handleIncomingMessage(context.Background(), telegramMessage{Text: "/approve req-1 investigate", Chat: telegramChat{ID: 1234}})
	if err != nil {
		t.Fatalf("handleIncomingMessage returned error: %v", err)
	}

	if approvalCalls != 0 {
		t.Fatalf("approvalCalls = %d, want 0", approvalCalls)
	}
	if len(sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(sent))
	}
	if !strings.Contains(sent[0], "Typed confirmation required") {
		t.Fatalf("expected typed confirmation message, got %q", sent[0])
	}
	if !strings.Contains(sent[0], "/confirm req-1") {
		t.Fatalf("expected /confirm instruction, got %q", sent[0])
	}
}

func TestConfirmFlowHonorsForbiddenDecisionPath(t *testing.T) {
	t.Parallel()

	var (
		mu            sync.Mutex
		approvalCalls int
	)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"permissions": map[string]any{"chat:use": map[string]any{"allowed": true}}})
		case r.URL.Path == "/api/v1/approvals" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"approvals": []map[string]any{{
					"metadata": map[string]any{"name": "req-1"},
					"spec":     map[string]any{"action": map[string]any{"tier": "destructive", "tool": "ssh.exec", "target": "castra"}},
					"status":   map[string]any{"phase": "Pending"},
				}},
			})
		case r.URL.Path == "/api/v1/approvals/req-1" && r.Method == http.MethodPost:
			mu.Lock()
			approvalCalls++
			mu.Unlock()
			http.Error(w, `{"error":"role viewer does not permit action approvals:decide"}`, http.StatusForbidden)
		default:
			http.Error(w, `{"error":"unexpected endpoint"}`, http.StatusInternalServerError)
		}
	}))
	defer apiSrv.Close()

	bot := mustNewTestBot(t, TelegramConfig{
		BotToken:   "test-token",
		APIBaseURL: apiSrv.URL,
		UserBindings: []UserBinding{
			{ChatID: 1234, Email: "viewer@example.com", Subject: "telegram:1234", Groups: []string{"legator-viewer"}},
		},
	})

	var sent []string
	bot.sendMessageFn = func(_ context.Context, _ int64, text string) error {
		sent = append(sent, text)
		return nil
	}

	err := bot.handleIncomingMessage(context.Background(), telegramMessage{Text: "/approve req-1 investigate", Chat: telegramChat{ID: 1234}})
	if err != nil {
		t.Fatalf("start approve returned error: %v", err)
	}
	code := extractConfirmationCode(t, sent[len(sent)-1], "req-1")

	err = bot.handleIncomingMessage(context.Background(), telegramMessage{Text: "/confirm req-1 " + code, Chat: telegramChat{ID: 1234}})
	if err != nil {
		t.Fatalf("confirm returned error: %v", err)
	}

	if !strings.Contains(sent[len(sent)-1], "/confirm failed") || !strings.Contains(sent[len(sent)-1], "api 403") {
		t.Fatalf("expected API forbidden response, got %q", sent[len(sent)-1])
	}

	mu.Lock()
	defer mu.Unlock()
	if approvalCalls != 1 {
		t.Fatalf("approvalCalls = %d, want 1", approvalCalls)
	}
}

func TestConfirmFlowExpires(t *testing.T) {
	t.Parallel()

	var approvalCalls int
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]any{"permissions": map[string]any{"chat:use": map[string]any{"allowed": true}}})
		case r.URL.Path == "/api/v1/approvals" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"approvals": []map[string]any{{
					"metadata": map[string]any{"name": "req-1"},
					"spec":     map[string]any{"action": map[string]any{"tier": "destructive", "tool": "ssh.exec", "target": "castra"}},
					"status":   map[string]any{"phase": "Pending"},
				}},
			})
		case r.URL.Path == "/api/v1/approvals/req-1" && r.Method == http.MethodPost:
			approvalCalls++
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, `{"error":"unexpected endpoint"}`, http.StatusInternalServerError)
		}
	}))
	defer apiSrv.Close()

	bot := mustNewTestBot(t, TelegramConfig{
		BotToken:        "test-token",
		APIBaseURL:      apiSrv.URL,
		ConfirmationTTL: 30 * time.Millisecond,
		UserBindings: []UserBinding{
			{ChatID: 1234, Email: "operator@example.com", Subject: "telegram:1234", Groups: []string{"legator-operator"}},
		},
	})

	var sent []string
	bot.sendMessageFn = func(_ context.Context, _ int64, text string) error {
		sent = append(sent, text)
		return nil
	}

	err := bot.handleIncomingMessage(context.Background(), telegramMessage{Text: "/approve req-1 investigate", Chat: telegramChat{ID: 1234}})
	if err != nil {
		t.Fatalf("start approve returned error: %v", err)
	}
	code := extractConfirmationCode(t, sent[len(sent)-1], "req-1")
	time.Sleep(60 * time.Millisecond)

	err = bot.handleIncomingMessage(context.Background(), telegramMessage{Text: "/confirm req-1 " + code, Chat: telegramChat{ID: 1234}})
	if err != nil {
		t.Fatalf("confirm returned error: %v", err)
	}

	if !strings.Contains(sent[len(sent)-1], "expired") {
		t.Fatalf("expected expired response, got %q", sent[len(sent)-1])
	}
	if approvalCalls != 0 {
		t.Fatalf("approvalCalls = %d, want 0", approvalCalls)
	}
}

func extractConfirmationCode(t *testing.T, message, approvalID string) string {
	t.Helper()
	fields := strings.Fields(message)
	for i := 0; i < len(fields)-2; i++ {
		if fields[i] == "/confirm" && fields[i+1] == approvalID {
			return fields[i+2]
		}
	}
	t.Fatalf("confirmation code not found in message: %q", message)
	return ""
}

func mustNewTestBot(t *testing.T, cfg TelegramConfig) *TelegramBot {
	t.Helper()
	bot, err := NewTelegramBot(cfg, logr.Discard())
	if err != nil {
		t.Fatalf("NewTelegramBot() error = %v", err)
	}
	return bot
}
