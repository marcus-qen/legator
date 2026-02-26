package webhook

import (
	"path/filepath"
	"testing"
)

func webhookTempDB(t *testing.T) string {
	return filepath.Join(t.TempDir(), "webhook.db")
}

func TestWebhookStoreRegisterAndList(t *testing.T) {
	s, err := NewStore(webhookTempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.Register(WebhookConfig{ID: "w1", URL: "http://example.com/hook", Events: []string{"probe.offline"}, Enabled: true})
	s.Register(WebhookConfig{ID: "w2", URL: "http://example.com/hook2", Events: []string{"command.failed"}, Enabled: true})

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("expected 2, got %d", len(list))
	}
}

func TestWebhookStorePersistsAcrossRestart(t *testing.T) {
	dbPath := webhookTempDB(t)

	s1, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s1.Register(WebhookConfig{ID: "w1", URL: "http://hook.example.com", Events: []string{"probe.offline", "approval.needed"}, Secret: "mysecret", Enabled: true})
	s1.Register(WebhookConfig{ID: "w2", URL: "http://other.example.com", Events: []string{"command.failed"}, Enabled: false})
	s1.Close()

	s2, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	list := s2.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 after restart, got %d", len(list))
	}

	found := map[string]WebhookConfig{}
	for _, w := range list {
		found[w.ID] = w
	}

	w1 := found["w1"]
	if w1.URL != "http://hook.example.com" || len(w1.Events) != 2 || w1.Secret != "mysecret" || !w1.Enabled {
		t.Fatalf("w1 not restored correctly: %+v", w1)
	}

	w2 := found["w2"]
	if w2.Enabled {
		t.Fatal("w2 should be disabled")
	}
}

func TestWebhookStoreRemove(t *testing.T) {
	s, err := NewStore(webhookTempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.Register(WebhookConfig{ID: "w1", URL: "http://example.com", Events: []string{"probe.offline"}, Enabled: true})
	s.Remove("w1")

	if len(s.List()) != 0 {
		t.Fatal("webhook should be removed")
	}
}
