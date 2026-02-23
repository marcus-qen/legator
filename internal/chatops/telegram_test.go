package chatops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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

func TestApprovalDecisionGoesThroughAPIAndHonorsForbidden(t *testing.T) {
	t.Parallel()

	var (
		mu            sync.Mutex
		approvalCalls int
		authorization string
	)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"permissions": map[string]any{
					"chat:use": map[string]any{"allowed": true},
				},
			})
		case r.URL.Path == "/api/v1/approvals/req-1" && r.Method == http.MethodPost:
			mu.Lock()
			approvalCalls++
			authorization = r.Header.Get("Authorization")
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

	err := bot.handleIncomingMessage(context.Background(), telegramMessage{
		Text: "/approve req-1 investigating",
		Chat: telegramChat{ID: 1234},
	})
	if err != nil {
		t.Fatalf("handleIncomingMessage returned error: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(sent))
	}
	if !strings.Contains(sent[0], "/approve failed") || !strings.Contains(sent[0], "api 403") {
		t.Fatalf("expected API forbidden response, got %q", sent[0])
	}

	mu.Lock()
	defer mu.Unlock()
	if approvalCalls != 1 {
		t.Fatalf("approvalCalls = %d, want 1", approvalCalls)
	}
	if !strings.HasPrefix(authorization, "Bearer ") {
		t.Fatalf("authorization header missing bearer token: %q", authorization)
	}
}

func mustNewTestBot(t *testing.T, cfg TelegramConfig) *TelegramBot {
	t.Helper()
	bot, err := NewTelegramBot(cfg, logr.Discard())
	if err != nil {
		t.Fatalf("NewTelegramBot() error = %v", err)
	}
	return bot
}
