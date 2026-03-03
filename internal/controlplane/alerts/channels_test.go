package alerts

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/fleet"
	"go.uber.org/zap"
)

func TestNormalizeChannelInputValidation(t *testing.T) {
	if _, err := normalizeChannelInput(NotificationChannel{Name: "", Type: ChannelTypeSlack}); err == nil {
		t.Fatal("expected name validation error")
	}

	if _, err := normalizeChannelInput(NotificationChannel{
		Name: "ops-email",
		Type: ChannelTypeEmail,
		Email: &EmailChannelConfig{
			SMTPHost: "smtp.example.com",
			SMTPPort: 587,
			From:     "alerts@example.com",
			To:       []string{"not-an-email"},
		},
	}); err == nil {
		t.Fatal("expected invalid recipient validation error")
	}

	channel, err := normalizeChannelInput(NotificationChannel{
		Name: "ops-slack",
		Type: ChannelTypeSlack,
		Slack: &SlackChannelConfig{
			WebhookURL: "https://hooks.slack.com/services/T/B/C",
			Channel:    "#incidents",
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("expected valid slack channel: %v", err)
	}
	if channel.Slack == nil || channel.Slack.WebhookURL == "" {
		t.Fatal("expected slack config to be preserved")
	}
}

func TestStoreNotificationChannelCRUD(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	defer func() { _ = store.Close() }()

	channel, err := normalizeChannelInput(NotificationChannel{
		Name:    "Primary Slack",
		Type:    ChannelTypeSlack,
		Enabled: true,
		Slack: &SlackChannelConfig{
			WebhookURL: "https://hooks.slack.com/services/T/B/C",
		},
	})
	if err != nil {
		t.Fatalf("normalizeChannelInput: %v", err)
	}

	created, err := store.CreateChannel(channel)
	if err != nil {
		t.Fatalf("CreateChannel error: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected channel ID")
	}

	fetched, err := store.GetChannel(created.ID)
	if err != nil {
		t.Fatalf("GetChannel error: %v", err)
	}
	if fetched.Name != "Primary Slack" {
		t.Fatalf("unexpected fetched channel name: %s", fetched.Name)
	}

	fetched.Name = "Secondary Slack"
	updated, err := store.UpdateChannel(*fetched)
	if err != nil {
		t.Fatalf("UpdateChannel error: %v", err)
	}
	if updated.Name != "Secondary Slack" {
		t.Fatalf("unexpected updated channel name: %s", updated.Name)
	}

	listed, err := store.ListChannels()
	if err != nil {
		t.Fatalf("ListChannels error: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(listed))
	}

	if err := store.DeleteChannel(created.ID); err != nil {
		t.Fatalf("DeleteChannel error: %v", err)
	}
	if _, err := store.GetChannel(created.ID); err == nil {
		t.Fatal("expected channel to be deleted")
	}
}

func TestHandleTestChannelAndEvaluateDelivery(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	defer func() { _ = store.Close() }()

	var hits atomic.Int32
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer testServer.Close()

	channel, err := normalizeChannelInput(NotificationChannel{
		Name:    "Slack Alerts",
		Type:    ChannelTypeSlack,
		Enabled: true,
		Slack: &SlackChannelConfig{
			WebhookURL: testServer.URL,
		},
	})
	if err != nil {
		t.Fatalf("normalizeChannelInput error: %v", err)
	}
	createdChannel, err := store.CreateChannel(channel)
	if err != nil {
		t.Fatalf("CreateChannel error: %v", err)
	}

	mgr := fleet.NewManager(zap.NewNop())
	engine := NewEngine(store, mgr, nil, nil, zap.NewNop())

	auditCh := make(chan NotificationAuditRecord, 4)
	engine.SetNotificationAuditRecorder(NotificationAuditRecorderFunc(func(record NotificationAuditRecord) {
		auditCh <- record
	}))

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/notification-channels/{id}/test", engine.HandleTestChannel)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/notification-channels/"+createdChannel.ID+"/test", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 from channel test, got %d body=%s", rr.Code, rr.Body.String())
	}

	waitFor(t, 2*time.Second, func() bool { return hits.Load() >= 1 }, "test notification delivery")

	rule, err := store.CreateRule(AlertRule{
		Name:    "probe offline",
		Enabled: true,
		Condition: AlertCondition{
			Type:     "probe_offline",
			Duration: "0s",
		},
		Actions: []AlertAction{{Type: "channel", ChannelID: createdChannel.ID}},
	})
	if err != nil {
		t.Fatalf("CreateRule error: %v", err)
	}

	probe := mgr.Register("probe-1", "host-1", "linux", "amd64")
	probe.Status = "offline"
	probe.LastSeen = time.Now().UTC().Add(-5 * time.Minute)

	if err := engine.Evaluate(); err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool { return hits.Load() >= 2 }, "rule channel delivery")

	// Ensure at least one delivery audit and one test audit were emitted.
	var sawTest, sawDelivery bool
	deadline := time.After(2 * time.Second)
	for !(sawTest && sawDelivery) {
		select {
		case rec := <-auditCh:
			if rec.Kind == NotificationAuditTest && rec.Success {
				sawTest = true
			}
			if rec.Kind == NotificationAuditDelivery && rec.RuleID == rule.ID {
				sawDelivery = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for notification audit records (sawTest=%v sawDelivery=%v)", sawTest, sawDelivery)
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", strings.TrimSpace(what))
}
