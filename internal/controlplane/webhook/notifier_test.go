package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/metrics"
)

func TestNotifier_RegisterRemoveList(t *testing.T) {
	n := &Notifier{}

	n.Register(WebhookConfig{ID: "a", URL: "https://example.com/a", Events: []string{"probe.offline"}, Enabled: true})
	n.Register(WebhookConfig{ID: "b", URL: "https://example.com/b", Events: []string{"command.failed"}, Enabled: true})

	if got := len(n.List()); got != 2 {
		t.Fatalf("len(list) = %d, want 2", got)
	}

	n.Remove("a")
	if got := len(n.List()); got != 1 {
		t.Fatalf("len(list) after remove = %d, want 1", got)
	}
}

func TestNotifier_NotifyDispatchesMatchingWebhooksOnly(t *testing.T) {
	n := NewNotifier()

	matching := make(chan struct{}, 2)
	ignored := make(chan struct{}, 2)

	matchingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		matching <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer matchingServer.Close()

	ignoredServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ignored <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer ignoredServer.Close()

	n.Register(WebhookConfig{ID: "match", URL: matchingServer.URL, Events: []string{"probe.offline"}, Enabled: true})
	n.Register(WebhookConfig{ID: "ignore", URL: ignoredServer.URL, Events: []string{"command.failed"}, Enabled: true})

	n.Notify("probe.offline", "probe-1", "summary", map[string]string{"status": "down"})

	if !awaitSignal(t, matching, 2*time.Second) {
		t.Fatalf("timed out waiting for matching webhook")
	}
	if awaitSignal(t, ignored, 150*time.Millisecond) {
		t.Fatalf("unexpected ignored webhook call")
	}
}

func TestNotifier_NotifyHMACSignature(t *testing.T) {
	n := NewNotifier()
	secret := "top-secret"

	payloads := make(chan []byte, 1)
	sig := make(chan string, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		payloads <- body
		sig <- r.Header.Get("X-Legator-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n.Register(WebhookConfig{ID: "sec", URL: server.URL, Events: []string{"command.failed"}, Secret: secret, Enabled: true})
	n.Notify("command.failed", "probe-2", "command failed", map[string]int{"exit": 1})

	var body []byte
	if !awaitSignalValue(t, payloads, &body, 2*time.Second) {
		t.Fatal("timed out waiting for webhook payload")
	}
	var gotSig string
	if !awaitSignalValue(t, sig, &gotSig, 2*time.Second) {
		t.Fatal("timed out waiting for signature header")
	}

	target := hmac.New(sha256.New, []byte(secret))
	target.Write(body)
	expectedSig := hex.EncodeToString(target.Sum(nil))
	if gotSig != expectedSig {
		t.Fatalf("signature = %q, want %q", gotSig, expectedSig)
	}
}

func TestNotifier_NotifySkipsNonMatchingEvents(t *testing.T) {
	n := NewNotifier()
	calls := make(chan struct{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n.Register(WebhookConfig{ID: "only-offline", URL: server.URL, Events: []string{"probe.offline"}, Enabled: true})
	n.Notify("approval.needed", "probe-3", "summary", nil)

	if awaitSignal(t, calls, 200*time.Millisecond) {
		t.Fatal("webhook should not receive non-matching event")
	}
}

func TestNotifier_NotifySkipsDisabledWebhooks(t *testing.T) {
	n := NewNotifier()
	calls := make(chan struct{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n.Register(WebhookConfig{ID: "disabled", URL: server.URL, Events: []string{"probe.offline"}, Enabled: false})
	n.Notify("probe.offline", "probe-4", "summary", nil)

	if awaitSignal(t, calls, 200*time.Millisecond) {
		t.Fatal("disabled webhook should not receive notifications")
	}
}

func TestNotifier_NotifyRetriesOnFailure(t *testing.T) {
	n := NewNotifier()
	hits := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	n.Register(WebhookConfig{ID: "retry", URL: server.URL, Events: []string{"probe.retry"}, Enabled: true})
	n.Notify("probe.retry", "probe-5", "summary", nil)

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(2 * time.Second)
	for {
		if hits == 2 {
			break
		}
		select {
		case <-ticker.C:
		case <-timeout:
			t.Fatalf("timed out waiting for retry. hits=%d", hits)
		}
	}

	if hits != 2 {
		t.Fatalf("webhook hits = %d, want 2", hits)
	}
}

func TestNotifier_HTTPHandlers_CRUD(t *testing.T) {
	n := NewNotifier()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/webhooks", n.ListWebhooks)
	mux.HandleFunc("POST /api/v1/webhooks", n.RegisterWebhook)
	mux.HandleFunc("GET /api/v1/webhooks/{id}", n.GetWebhook)
	mux.HandleFunc("DELETE /api/v1/webhooks/{id}", n.DeleteWebhook)

	payload := `{"id":"my-webhook","url":"https://example.test/hook","events":["probe.offline"],"enabled":true}`
	regReq := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks", strings.NewReader(payload))
	regReq.Header.Set("Content-Type", "application/json")
	regResp := httptest.NewRecorder()
	mux.ServeHTTP(regResp, regReq)
	if regResp.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want %d", regResp.Code, http.StatusCreated)
	}

	var created WebhookConfig
	if err := json.NewDecoder(regResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created webhook: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/webhooks", nil)
	listResp := httptest.NewRecorder()
	mux.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d", listResp.Code, http.StatusOK)
	}
	var listed []WebhookConfig
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("list count = %d, want 1", len(listed))
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/webhooks/"+created.ID, nil)
	getResp := httptest.NewRecorder()
	mux.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d", getResp.Code, http.StatusOK)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/webhooks/"+created.ID, nil)
	delResp := httptest.NewRecorder()
	mux.ServeHTTP(delResp, delReq)
	if delResp.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d", delResp.Code, http.StatusNoContent)
	}

	listResp = httptest.NewRecorder()
	mux.ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("post-delete list status = %d, want %d", listResp.Code, http.StatusOK)
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode post-delete list: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("post-delete list count = %d, want 0", len(listed))
	}
}

func TestNotifier_HTTPHandlers_TestEndpoint(t *testing.T) {
	n := NewNotifier()
	received := make(chan struct{}, 1)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/webhooks/{id}/test", n.TestWebhook)

	n.Register(WebhookConfig{ID: "test-id", URL: target.URL, Events: []string{"probe.offline"}, Enabled: true})

	testReq := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/test-id/test", nil)
	testResp := httptest.NewRecorder()
	mux.ServeHTTP(testResp, testReq)
	if testResp.Code != http.StatusOK {
		t.Fatalf("test status = %d, want %d", testResp.Code, http.StatusOK)
	}
	if !awaitSignal(t, received, 2*time.Second) {
		t.Fatal("timed out waiting for test request")
	}
}

func TestNotifier_HTTPHandlers_DeliveryHistoryEndpoint(t *testing.T) {
	n := NewNotifier()
	received := make(chan struct{}, 1)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer target.Close()

	n.Register(WebhookConfig{ID: "delivery-id", URL: target.URL + "/path?token=secret", Events: []string{"probe.offline"}, Enabled: true})
	n.Notify("probe.offline", "probe-1", "summary", map[string]string{"status": "down"})

	if !awaitSignal(t, received, 2*time.Second) {
		t.Fatal("timed out waiting for webhook delivery")
	}
	waitForDeliveries(t, n, 1, 2*time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/webhooks/deliveries", n.ListDeliveries)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/webhooks/deliveries?limit=1", nil)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("history status = %d, want %d", resp.Code, http.StatusOK)
	}

	var payload struct {
		Deliveries []DeliveryRecord `json:"deliveries"`
		Count      int              `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	if payload.Count != 1 || len(payload.Deliveries) != 1 {
		t.Fatalf("unexpected history payload: %+v", payload)
	}

	delivery := payload.Deliveries[0]
	if delivery.EventType != "probe.offline" {
		t.Fatalf("event_type = %q, want probe.offline", delivery.EventType)
	}
	if !strings.HasSuffix(delivery.TargetURL, "/***") {
		t.Fatalf("target_url should be masked, got %q", delivery.TargetURL)
	}
	if delivery.StatusCode != http.StatusAccepted {
		t.Fatalf("status_code = %d, want %d", delivery.StatusCode, http.StatusAccepted)
	}
	if delivery.DurationMS < 0 {
		t.Fatalf("duration_ms should be non-negative, got %d", delivery.DurationMS)
	}
	if delivery.Timestamp.IsZero() {
		t.Fatal("timestamp should be set")
	}
}

func TestNotifier_MetricsObserver_RecordsWebhookDelivery(t *testing.T) {
	n := NewNotifier()
	collector := metrics.NewCollector(&metricsTestFleet{}, &metricsTestHub{}, &metricsTestApprovals{}, &metricsTestAudit{})
	n.SetDeliveryObserver(collector)

	received := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	n.Register(WebhookConfig{ID: "metrics-id", URL: target.URL, Events: []string{"probe.offline"}, Enabled: true})
	n.Notify("probe.offline", "probe-2", "summary", nil)

	if !awaitSignal(t, received, 2*time.Second) {
		t.Fatal("timed out waiting for webhook delivery")
	}

	waitForMetric(t, collector, `legator_webhooks_sent_total{event_type="probe.offline",status="success"} 1`, 2*time.Second)
	waitForMetric(t, collector, `legator_webhook_duration_seconds_count{event_type="probe.offline"} 1`, 2*time.Second)
}

func awaitSignal(t *testing.T, ch <-chan struct{}, timeout time.Duration) bool {
	t.Helper()
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

func awaitSignalValue[T any](t *testing.T, ch <-chan T, out *T, timeout time.Duration) bool {
	t.Helper()
	select {
	case v := <-ch:
		*out = v
		return true
	case <-time.After(timeout):
		return false
	}
}

func waitForDeliveries(t *testing.T, n *Notifier, want int, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(n.Deliveries(want)) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %d delivery records", want)
}

func waitForMetric(t *testing.T, c *metrics.Collector, needle string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics", nil)
		resp := httptest.NewRecorder()
		c.Handler().ServeHTTP(resp, req)

		if strings.Contains(resp.Body.String(), needle) {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for metric %q", needle)
}

type metricsTestFleet struct{}

func (m *metricsTestFleet) Count() map[string]int     { return map[string]int{} }
func (m *metricsTestFleet) TagCounts() map[string]int { return map[string]int{} }

type metricsTestHub struct{}

func (m *metricsTestHub) Connected() int { return 0 }

type metricsTestApprovals struct{}

func (m *metricsTestApprovals) PendingCount() int { return 0 }

type metricsTestAudit struct{}

func (m *metricsTestAudit) Count() int { return 0 }
