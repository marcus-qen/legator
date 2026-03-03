package alerts

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// NotificationAuditKind labels the reason a notification audit record was emitted.
type NotificationAuditKind string

const (
	NotificationAuditDelivery NotificationAuditKind = "delivery"
	NotificationAuditTest     NotificationAuditKind = "test"
)

// NotificationAuditRecord captures one notification delivery/test outcome.
type NotificationAuditRecord struct {
	Kind        NotificationAuditKind `json:"kind"`
	Success     bool                  `json:"success"`
	ChannelID   string                `json:"channel_id"`
	ChannelName string                `json:"channel_name,omitempty"`
	ChannelType string                `json:"channel_type"`
	RuleID      string                `json:"rule_id,omitempty"`
	RuleName    string                `json:"rule_name,omitempty"`
	ProbeID     string                `json:"probe_id,omitempty"`
	EventType   string                `json:"event_type,omitempty"`
	Error       string                `json:"error,omitempty"`
}

// NotificationAuditRecorder consumes notification audit records.
type NotificationAuditRecorder interface {
	RecordNotification(record NotificationAuditRecord)
}

// NotificationAuditRecorderFunc adapts a function to NotificationAuditRecorder.
type NotificationAuditRecorderFunc func(record NotificationAuditRecord)

// RecordNotification invokes f(record).
func (f NotificationAuditRecorderFunc) RecordNotification(record NotificationAuditRecord) {
	if f == nil {
		return
	}
	f(record)
}

// SetNotificationAuditRecorder sets an optional audit recorder for channel deliveries/tests.
func (e *Engine) SetNotificationAuditRecorder(recorder NotificationAuditRecorder) {
	e.auditRecorder = recorder
}

type notificationMessage struct {
	EventType string
	Summary   string
	ProbeID   string
	RuleID    string
	RuleName  string
	Detail    any
}

// HandleListChannels serves GET /api/v1/notification-channels.
func (e *Engine) HandleListChannels(w http.ResponseWriter, r *http.Request) {
	if e.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "alerts store unavailable")
		return
	}
	channels, err := e.store.ListChannels()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, channels)
}

// HandleCreateChannel serves POST /api/v1/notification-channels.
func (e *Engine) HandleCreateChannel(w http.ResponseWriter, r *http.Request) {
	if e.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "alerts store unavailable")
		return
	}

	var req NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	normalized, err := normalizeChannelInput(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_channel", err.Error())
		return
	}

	created, err := e.store.CreateChannel(normalized)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, created)
}

// HandleGetChannel serves GET /api/v1/notification-channels/{id}.
func (e *Engine) HandleGetChannel(w http.ResponseWriter, r *http.Request) {
	if e.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "alerts store unavailable")
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing channel id")
		return
	}

	channel, err := e.store.GetChannel(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "channel not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, channel)
}

// HandleUpdateChannel serves PUT /api/v1/notification-channels/{id}.
func (e *Engine) HandleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if e.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "alerts store unavailable")
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing channel id")
		return
	}

	existing, err := e.store.GetChannel(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "channel not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	var req NotificationChannel
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	req.ID = id
	req.CreatedAt = existing.CreatedAt

	normalized, err := normalizeChannelInput(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_channel", err.Error())
		return
	}

	updated, err := e.store.UpdateChannel(normalized)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "channel not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

// HandleDeleteChannel serves DELETE /api/v1/notification-channels/{id}.
func (e *Engine) HandleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	if e.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "alerts store unavailable")
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing channel id")
		return
	}

	inUseBy, err := e.rulesReferencingChannel(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if len(inUseBy) > 0 {
		writeError(w, http.StatusConflict, "channel_in_use", fmt.Sprintf("channel is referenced by rule %s", inUseBy[0]))
		return
	}

	if err := e.store.DeleteChannel(id); err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "channel not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleTestChannel serves POST /api/v1/notification-channels/{id}/test.
func (e *Engine) HandleTestChannel(w http.ResponseWriter, r *http.Request) {
	if e.store == nil {
		writeError(w, http.StatusServiceUnavailable, "service_unavailable", "alerts store unavailable")
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "missing channel id")
		return
	}

	channel, err := e.store.GetChannel(id)
	if err != nil {
		if IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "channel not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !channel.Enabled {
		writeError(w, http.StatusBadRequest, "invalid_channel", "channel is disabled")
		return
	}

	msg := notificationMessage{
		EventType: "notification.test",
		Summary:   fmt.Sprintf("[TEST] Legator notification channel %s", channel.Name),
		RuleID:    "test",
		RuleName:  "test",
		Detail: map[string]any{
			"channel_id": channel.ID,
			"channel":    channel.Name,
			"timestamp":  time.Now().UTC().Format(time.RFC3339),
		},
	}

	if err := e.sendToChannel(*channel, msg); err != nil {
		e.recordNotificationAudit(NotificationAuditRecord{
			Kind:        NotificationAuditTest,
			Success:     false,
			ChannelID:   channel.ID,
			ChannelName: channel.Name,
			ChannelType: channel.Type,
			EventType:   msg.EventType,
			Error:       err.Error(),
		})
		writeError(w, http.StatusBadGateway, "delivery_failed", err.Error())
		return
	}

	e.recordNotificationAudit(NotificationAuditRecord{
		Kind:        NotificationAuditTest,
		Success:     true,
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
		ChannelType: channel.Type,
		EventType:   msg.EventType,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "sent",
		"channel": channel.ID,
	})
}

func (e *Engine) rulesReferencingChannel(channelID string) ([]string, error) {
	if e.store == nil {
		return nil, nil
	}
	rules, err := e.store.ListRules()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	for _, rule := range rules {
		for _, action := range rule.Actions {
			if action.Type == "channel" && strings.TrimSpace(action.ChannelID) == channelID {
				out = append(out, rule.ID)
				break
			}
		}
	}
	return out, nil
}

func (e *Engine) deliverNotificationChannels(rule AlertRule, evt AlertEvent, evtType string) {
	if e.store == nil {
		return
	}

	wanted := make(map[string]struct{})
	for _, action := range rule.Actions {
		if action.Type != "channel" {
			continue
		}
		id := strings.TrimSpace(action.ChannelID)
		if id == "" {
			continue
		}
		wanted[id] = struct{}{}
	}
	if len(wanted) == 0 {
		return
	}

	channels, err := e.store.ListChannels()
	if err != nil {
		for channelID := range wanted {
			e.recordNotificationAudit(NotificationAuditRecord{
				Kind:      NotificationAuditDelivery,
				Success:   false,
				ChannelID: channelID,
				RuleID:    rule.ID,
				RuleName:  rule.Name,
				ProbeID:   evt.ProbeID,
				EventType: evtType,
				Error:     err.Error(),
			})
		}
		return
	}

	channelsByID := make(map[string]NotificationChannel, len(channels))
	for _, channel := range channels {
		channelsByID[channel.ID] = channel
	}

	for channelID := range wanted {
		channel, ok := channelsByID[channelID]
		if !ok {
			e.recordNotificationAudit(NotificationAuditRecord{
				Kind:      NotificationAuditDelivery,
				Success:   false,
				ChannelID: channelID,
				RuleID:    rule.ID,
				RuleName:  rule.Name,
				ProbeID:   evt.ProbeID,
				EventType: evtType,
				Error:     "channel not found",
			})
			continue
		}
		if !channel.Enabled {
			e.recordNotificationAudit(NotificationAuditRecord{
				Kind:        NotificationAuditDelivery,
				Success:     false,
				ChannelID:   channel.ID,
				ChannelName: channel.Name,
				ChannelType: channel.Type,
				RuleID:      rule.ID,
				RuleName:    rule.Name,
				ProbeID:     evt.ProbeID,
				EventType:   evtType,
				Error:       "channel disabled",
			})
			continue
		}

		message := notificationMessage{
			EventType: evtType,
			Summary:   fmt.Sprintf("[%s] %s", strings.ToUpper(evt.Status), evt.Message),
			ProbeID:   evt.ProbeID,
			RuleID:    rule.ID,
			RuleName:  rule.Name,
			Detail:    evt,
		}

		ch := channel
		go func() {
			err := e.sendToChannel(ch, message)
			record := NotificationAuditRecord{
				Kind:        NotificationAuditDelivery,
				Success:     err == nil,
				ChannelID:   ch.ID,
				ChannelName: ch.Name,
				ChannelType: ch.Type,
				RuleID:      rule.ID,
				RuleName:    rule.Name,
				ProbeID:     evt.ProbeID,
				EventType:   evtType,
			}
			if err != nil {
				record.Error = err.Error()
			}
			e.recordNotificationAudit(record)
		}()
	}
}

func (e *Engine) sendToChannel(channel NotificationChannel, msg notificationMessage) error {
	switch channel.Type {
	case ChannelTypeSlack:
		return e.sendSlack(channel, msg)
	case ChannelTypeEmail:
		return e.sendEmail(channel, msg)
	case ChannelTypePagerDuty:
		return e.sendPagerDuty(channel, msg)
	default:
		return fmt.Errorf("unsupported channel type: %s", channel.Type)
	}
}

func (e *Engine) sendSlack(channel NotificationChannel, msg notificationMessage) error {
	if channel.Slack == nil {
		return fmt.Errorf("slack config missing")
	}

	body := map[string]any{"text": msg.Summary}
	if strings.TrimSpace(channel.Slack.Channel) != "" {
		body["channel"] = channel.Slack.Channel
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, channel.Slack.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send slack webhook: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned status %d", resp.StatusCode)
	}
	return nil
}

func (e *Engine) sendEmail(channel NotificationChannel, msg notificationMessage) error {
	if channel.Email == nil {
		return fmt.Errorf("email config missing")
	}

	cfg := channel.Email
	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.SMTPPort)

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}

	client, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: cfg.SMTPHost,
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}

	if strings.TrimSpace(cfg.Username) != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.SMTPHost)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	for _, recipient := range cfg.To {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("smtp recipient %s: %w", recipient, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}

	subject := "[Legator] Alert notification"
	if msg.EventType == "notification.test" {
		subject = "[Legator] Test notification"
	}
	body := fmt.Sprintf("Event: %s\nRule: %s (%s)\nProbe: %s\n\n%s\n", msg.EventType, msg.RuleName, msg.RuleID, msg.ProbeID, msg.Summary)
	raw := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", cfg.From, strings.Join(cfg.To, ", "), subject, body)

	if _, err := io.WriteString(w, raw); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close body: %w", err)
	}

	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}

func (e *Engine) sendPagerDuty(channel NotificationChannel, msg notificationMessage) error {
	if channel.PagerDuty == nil {
		return fmt.Errorf("pagerduty config missing")
	}

	endpoint := defaultPagerDutyEventsAPIURL
	if strings.TrimSpace(channel.PagerDuty.EventsAPIURL) != "" {
		endpoint = channel.PagerDuty.EventsAPIURL
	}

	payload := map[string]any{
		"routing_key":  channel.PagerDuty.IntegrationKey,
		"event_action": "trigger",
		"payload": map[string]any{
			"summary":  msg.Summary,
			"source":   coalesce(msg.ProbeID, "legator"),
			"severity": "critical",
			"custom_details": map[string]any{
				"event_type": msg.EventType,
				"rule_id":    msg.RuleID,
				"rule_name":  msg.RuleName,
				"probe_id":   msg.ProbeID,
				"detail":     msg.Detail,
			},
		},
	}

	blob, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal pagerduty payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(blob))
	if err != nil {
		return fmt.Errorf("build pagerduty request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send pagerduty event: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("pagerduty API returned status %d", resp.StatusCode)
	}
	return nil
}

func (e *Engine) recordNotificationAudit(record NotificationAuditRecord) {
	if e.auditRecorder == nil {
		return
	}
	e.auditRecorder.RecordNotification(record)
}

func coalesce(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
