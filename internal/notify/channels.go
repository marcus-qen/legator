/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package notify implements notification delivery to external channels.
// Agents publish findings; the notification system routes them to
// Slack, Telegram, email, or generic webhooks based on severity.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// Channel is the interface for all notification backends.
type Channel interface {
	// Send delivers a notification. Returns an error if delivery fails.
	Send(ctx context.Context, msg Message) error

	// Type returns the channel type name.
	Type() string
}

// Message is a notification to be delivered.
type Message struct {
	AgentName string
	RunName   string
	Severity  string // info, warning, critical
	Title     string
	Body      string
	Timestamp time.Time
}

// --- Slack ---

// SlackChannel sends notifications to Slack via webhook.
type SlackChannel struct {
	WebhookURL string
	Channel    string // optional override
	client     *http.Client
}

// NewSlackChannel creates a Slack notification channel.
func NewSlackChannel(webhookURL, channel string) *SlackChannel {
	return &SlackChannel{
		WebhookURL: webhookURL,
		Channel:    channel,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *SlackChannel) Type() string { return "slack" }

func (s *SlackChannel) Send(ctx context.Context, msg Message) error {
	emoji := severityEmoji(msg.Severity)
	text := fmt.Sprintf("%s *[%s] %s* — %s\n%s", emoji, strings.ToUpper(msg.Severity), msg.AgentName, msg.Title, msg.Body)

	payload := map[string]interface{}{
		"text": text,
	}
	if s.Channel != "" {
		payload["channel"] = s.Channel
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", s.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("slack returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// --- Telegram ---

// TelegramChannel sends notifications via Telegram Bot API.
type TelegramChannel struct {
	BotToken string
	ChatID   string
	client   *http.Client
}

// NewTelegramChannel creates a Telegram notification channel.
func NewTelegramChannel(botToken, chatID string) *TelegramChannel {
	return &TelegramChannel{
		BotToken: botToken,
		ChatID:   chatID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (t *TelegramChannel) Type() string { return "telegram" }

func (t *TelegramChannel) Send(ctx context.Context, msg Message) error {
	emoji := severityEmoji(msg.Severity)
	text := fmt.Sprintf("%s *\\[%s\\] %s*\n%s\n\n%s",
		emoji,
		strings.ToUpper(escapeMarkdown(msg.Severity)),
		escapeMarkdown(msg.AgentName),
		escapeMarkdown(msg.Title),
		escapeMarkdown(msg.Body),
	)

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.BotToken)
	payload := map[string]interface{}{
		"chat_id":    t.ChatID,
		"text":       text,
		"parse_mode": "MarkdownV2",
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// --- Email ---

// EmailChannel sends notifications via SMTP.
type EmailChannel struct {
	Host     string
	Port     int
	From     string
	To       []string
	Username string
	Password string
}

// NewEmailChannel creates an email notification channel.
func NewEmailChannel(host string, port int, from string, to []string, username, password string) *EmailChannel {
	return &EmailChannel{
		Host:     host,
		Port:     port,
		From:     from,
		To:       to,
		Username: username,
		Password: password,
	}
}

func (e *EmailChannel) Type() string { return "email" }

func (e *EmailChannel) Send(ctx context.Context, msg Message) error {
	subject := fmt.Sprintf("[Legator %s] %s — %s", strings.ToUpper(msg.Severity), msg.AgentName, msg.Title)
	body := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s\n\nAgent: %s\nRun: %s\nTime: %s",
		e.From,
		strings.Join(e.To, ","),
		subject,
		msg.Body,
		msg.AgentName,
		msg.RunName,
		msg.Timestamp.Format(time.RFC3339),
	)

	addr := fmt.Sprintf("%s:%d", e.Host, e.Port)
	var auth smtp.Auth
	if e.Username != "" {
		auth = smtp.PlainAuth("", e.Username, e.Password, e.Host)
	}

	return smtp.SendMail(addr, auth, e.From, e.To, []byte(body))
}

// --- Webhook ---

// WebhookChannel sends JSON notifications to any HTTP endpoint.
type WebhookChannel struct {
	URL     string
	Headers map[string]string // optional auth headers
	client  *http.Client
}

// NewWebhookChannel creates a generic webhook notification channel.
func NewWebhookChannel(url string, headers map[string]string) *WebhookChannel {
	return &WebhookChannel{
		URL:     url,
		Headers: headers,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

func (w *WebhookChannel) Type() string { return "webhook" }

func (w *WebhookChannel) Send(ctx context.Context, msg Message) error {
	payload := map[string]interface{}{
		"agent":     msg.AgentName,
		"run":       msg.RunName,
		"severity":  msg.Severity,
		"title":     msg.Title,
		"body":      msg.Body,
		"timestamp": msg.Timestamp.Format(time.RFC3339),
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", w.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range w.Headers {
		req.Header.Set(k, v)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// --- Router ---

// SeverityRoute maps severity levels to channels.
type SeverityRoute struct {
	Info     []Channel
	Warning  []Channel
	Critical []Channel
}

// Router dispatches notifications to channels based on severity.
type Router struct {
	routes   SeverityRoute
	limiter  *RateLimiter
	log      logr.Logger
}

// NewRouter creates a notification router.
func NewRouter(routes SeverityRoute, limiter *RateLimiter, log logr.Logger) *Router {
	return &Router{routes: routes, limiter: limiter, log: log}
}

// Notify sends a message to all channels matching its severity.
func (r *Router) Notify(ctx context.Context, msg Message) []error {
	channels := r.channelsForSeverity(msg.Severity)
	if len(channels) == 0 {
		return nil
	}

	// Check rate limit
	if r.limiter != nil && !r.limiter.Allow(msg.AgentName) {
		r.log.Info("notification rate-limited", "agent", msg.AgentName)
		return nil
	}

	var errs []error
	for _, ch := range channels {
		if err := ch.Send(ctx, msg); err != nil {
			r.log.Error(err, "notification failed", "type", ch.Type(), "agent", msg.AgentName)
			errs = append(errs, err)
		} else {
			r.log.Info("notification sent", "type", ch.Type(), "agent", msg.AgentName, "severity", msg.Severity)
		}
	}
	return errs
}

func (r *Router) channelsForSeverity(severity string) []Channel {
	switch severity {
	case "critical":
		// Critical goes to all levels
		var all []Channel
		all = append(all, r.routes.Critical...)
		all = append(all, r.routes.Warning...)
		all = append(all, r.routes.Info...)
		return all
	case "warning":
		var all []Channel
		all = append(all, r.routes.Warning...)
		all = append(all, r.routes.Info...)
		return all
	case "info":
		return r.routes.Info
	default:
		return r.routes.Info
	}
}

// --- Rate Limiter ---

// RateLimiter limits notifications per agent per hour.
type RateLimiter struct {
	maxPerHour int
	mu         sync.Mutex
	counts     map[string][]time.Time
}

// NewRateLimiter creates a rate limiter with the given max per hour per agent.
func NewRateLimiter(maxPerHour int) *RateLimiter {
	return &RateLimiter{
		maxPerHour: maxPerHour,
		counts:     make(map[string][]time.Time),
	}
}

// Allow checks if the agent is within rate limits.
func (rl *RateLimiter) Allow(agentName string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-1 * time.Hour)

	// Prune old entries
	recent := make([]time.Time, 0)
	for _, t := range rl.counts[agentName] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}

	if len(recent) >= rl.maxPerHour {
		return false
	}

	rl.counts[agentName] = append(recent, now)
	return true
}

// --- Helpers ---

func severityEmoji(severity string) string {
	switch severity {
	case "critical":
		return "🔴"
	case "warning":
		return "🟡"
	case "info":
		return "🔵"
	default:
		return "⚪"
	}
}

func escapeMarkdown(s string) string {
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(s)
}
