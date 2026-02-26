package webhook

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// ListWebhooks handles GET /api/v1/webhooks.
func (n *Notifier) ListWebhooks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, n.List())
}

// RegisterWebhook handles POST /api/v1/webhooks.
func (n *Notifier) RegisterWebhook(w http.ResponseWriter, r *http.Request) {
	var cfg WebhookConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request payload")
		return
	}
	if cfg.ID == "" {
		cfg.ID = uuid.NewString()
	}

	n.Register(cfg)
	writeJSON(w, http.StatusCreated, cfg)
}

// GetWebhook handles GET /api/v1/webhooks/{id}.
func (n *Notifier) GetWebhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg, ok := n.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("webhook not found: %s", id))
		return
	}

	writeJSON(w, http.StatusOK, cfg)
}

// DeleteWebhook handles DELETE /api/v1/webhooks/{id}.
func (n *Notifier) DeleteWebhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := n.get(id); !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("webhook not found: %s", id))
		return
	}

	n.Remove(id)
	w.WriteHeader(http.StatusNoContent)
}

// TestWebhook handles POST /api/v1/webhooks/{id}/test.
func (n *Notifier) TestWebhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg, ok := n.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("webhook not found: %s", id))
		return
	}

	testPayload := WebhookPayload{
		ID:        cfg.ID,
		Event:     "webhook.test",
		Timestamp: time.Now(),
		Summary:   "test webhook",
		Detail:    map[string]string{"id": cfg.ID},
	}

	if err := n.sendPayloadWithRetries(cfg, testPayload); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
