package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
)

const defaultDeliveryHistoryLimit = 100

// DeliveryObserver records webhook delivery outcomes.
type DeliveryObserver interface {
	RecordWebhookDelivery(eventType string, statusCode int, duration time.Duration, err error)
}

// DeliveryRecord captures one webhook delivery attempt.
type DeliveryRecord struct {
	Timestamp  time.Time `json:"timestamp"`
	EventType  string    `json:"event_type"`
	TargetURL  string    `json:"target_url"`
	StatusCode int       `json:"status_code"`
	DurationMS int64     `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
}

// WebhookConfig holds a registered webhook endpoint.
type WebhookConfig struct {
	ID      string   `json:"id"`
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	Secret  string   `json:"secret,omitempty"`
	Enabled bool     `json:"enabled"`
}

// WebhookPayload is the JSON body sent to webhook endpoints.
type WebhookPayload struct {
	ID        string    `json:"id"`
	Event     string    `json:"event"`
	Timestamp time.Time `json:"timestamp"`
	ProbeID   string    `json:"probe_id,omitempty"`
	Summary   string    `json:"summary"`
	Detail    any       `json:"detail,omitempty"`
}

// Notifier manages webhook registrations and dispatch.
type Notifier struct {
	mu         sync.RWMutex
	items      map[string]WebhookConfig
	httpClient *http.Client
	observer   DeliveryObserver

	deliveryMu sync.RWMutex
	deliveries []DeliveryRecord
}

// NewNotifier creates a new notifier with sane defaults.
func NewNotifier() *Notifier {
	return &Notifier{
		items:      make(map[string]WebhookConfig),
		httpClient: &http.Client{Timeout: 5 * time.Second},
		deliveries: make([]DeliveryRecord, 0, defaultDeliveryHistoryLimit),
	}
}

// SetDeliveryObserver registers an optional delivery observer.
func (n *Notifier) SetDeliveryObserver(observer DeliveryObserver) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.observer = observer
}

// Register adds or updates a webhook configuration.
func (n *Notifier) Register(cfg WebhookConfig) {
	if cfg.ID == "" {
		cfg.ID = uuid.NewString()
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	if n.items == nil {
		n.items = make(map[string]WebhookConfig)
	}
	n.items[cfg.ID] = cfg
}

// Remove deletes a webhook configuration.
func (n *Notifier) Remove(id string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	delete(n.items, id)
}

// List returns all registered webhook configurations.
func (n *Notifier) List() []WebhookConfig {
	n.mu.RLock()
	defer n.mu.RUnlock()

	out := make([]WebhookConfig, 0, len(n.items))
	for _, cfg := range n.items {
		out = append(out, cfg)
	}
	return out
}

// Deliveries returns the most recent delivery attempts (newest first).
func (n *Notifier) Deliveries(limit int) []DeliveryRecord {
	n.deliveryMu.RLock()
	defer n.deliveryMu.RUnlock()

	if limit <= 0 || limit > len(n.deliveries) {
		limit = len(n.deliveries)
	}

	out := make([]DeliveryRecord, 0, limit)
	for i := len(n.deliveries) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, n.deliveries[i])
	}
	return out
}

// Notify sends payloads to all enabled webhooks matching the event.
func (n *Notifier) Notify(event, probeID, summary string, detail any) {
	n.mu.RLock()
	webhooks := make([]WebhookConfig, 0, len(n.items))
	for _, cfg := range n.items {
		if !cfg.Enabled {
			continue
		}
		if !containsEvent(cfg.Events, event) {
			continue
		}

		webhooks = append(webhooks, cfg)
	}
	n.mu.RUnlock()

	if len(webhooks) == 0 {
		return
	}

	timestamp := time.Now()
	for _, cfg := range webhooks {
		payload := WebhookPayload{
			ID:        cfg.ID,
			Event:     event,
			Timestamp: timestamp,
			ProbeID:   probeID,
			Summary:   summary,
			Detail:    detail,
		}
		webhook := cfg
		go func() {
			started := time.Now()
			statusCode, err := n.sendPayloadWithRetries(webhook, payload)
			n.recordDelivery(payload.Event, webhook.URL, statusCode, time.Since(started), err)
		}()
	}
}

// sendPayloadWithRetries posts a payload to one webhook endpoint, retrying once on failure.
func (n *Notifier) sendPayloadWithRetries(cfg WebhookConfig, payload WebhookPayload) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal webhook payload: %w", err)
	}

	httpClient := n.client()

	var (
		lastErr    error
		statusCode int
	)
	for attempt := 1; attempt <= 2; attempt++ {
		req, err := http.NewRequest(http.MethodPost, cfg.URL, bytes.NewReader(body))
		if err != nil {
			return 0, fmt.Errorf("webhook request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if cfg.Secret != "" {
			req.Header.Set("X-Legator-Signature", signature(cfg.Secret, body))
		}

		resp, err := httpClient.Do(req)
		if err == nil {
			statusCode = resp.StatusCode
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return statusCode, nil
			}
			lastErr = fmt.Errorf("webhook returned status %d", resp.StatusCode)
		} else {
			lastErr = err
		}

		if attempt == 1 {
			// one retry after initial attempt
			continue
		}
	}

	return statusCode, lastErr
}

func (n *Notifier) recordDelivery(eventType, targetURL string, statusCode int, duration time.Duration, err error) {
	record := DeliveryRecord{
		Timestamp:  time.Now().UTC(),
		EventType:  eventType,
		TargetURL:  maskTargetURL(targetURL),
		StatusCode: statusCode,
		DurationMS: duration.Milliseconds(),
	}
	if err != nil {
		record.Error = err.Error()
	}

	n.deliveryMu.Lock()
	n.deliveries = append(n.deliveries, record)
	if len(n.deliveries) > defaultDeliveryHistoryLimit {
		offset := len(n.deliveries) - defaultDeliveryHistoryLimit
		copy(n.deliveries, n.deliveries[offset:])
		n.deliveries = n.deliveries[:defaultDeliveryHistoryLimit]
	}
	n.deliveryMu.Unlock()

	observer := n.deliveryObserver()
	if observer != nil {
		observer.RecordWebhookDelivery(eventType, statusCode, duration, err)
	}
}

func (n *Notifier) deliveryObserver() DeliveryObserver {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.observer
}

func (n *Notifier) get(id string) (WebhookConfig, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	cfg, ok := n.items[id]
	return cfg, ok
}

func (n *Notifier) client() *http.Client {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.httpClient != nil {
		return n.httpClient
	}

	return &http.Client{Timeout: 5 * time.Second}
}

func containsEvent(events []string, target string) bool {
	for _, e := range events {
		if e == target {
			return true
		}
	}
	return false
}

func maskTargetURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "***"
	}

	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
	}

	return fmt.Sprintf("%s://%s/***", scheme, u.Host)
}

func signature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
