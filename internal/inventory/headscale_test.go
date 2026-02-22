package inventory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
)

func TestHeadscaleSync_Sync(t *testing.T) {
	nodes := HeadscaleNodesResponse{
		Nodes: []HeadscaleNode{
			{
				ID:          "1",
				Name:        "centos7-proxy-01",
				GivenName:   "centos7-proxy-01",
				IPAddresses: []string{"100.64.0.15"},
				Online:      true,
				ForcedTags:  []string{"tag:managed-servers", "tag:legacy"},
			},
			{
				ID:          "2",
				Name:        "staging-db-01",
				GivenName:   "staging-db-01",
				IPAddresses: []string{"100.64.0.20"},
				Online:      true,
				ForcedTags:  []string{"tag:managed-databases"},
			},
			{
				ID:          "3",
				Name:        "offline-server",
				GivenName:   "offline-server",
				IPAddresses: []string{"100.64.0.30"},
				Online:      false,
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/node" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing auth header")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(nodes)
	}))
	defer server.Close()

	sync := NewHeadscaleSync(HeadscaleSyncConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
	}, logr.Discard())

	if err := sync.Sync(context.Background()); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	if got := sync.DeviceCount(); got != 3 {
		t.Errorf("DeviceCount = %d, want 3", got)
	}

	// Check centos7-proxy-01
	d, ok := sync.GetDevice("centos7-proxy-01")
	if !ok {
		t.Fatal("centos7-proxy-01 not found")
	}
	if d.Addresses.Headscale != "100.64.0.15" {
		t.Errorf("IP = %q, want 100.64.0.15", d.Addresses.Headscale)
	}
	if !d.Connectivity.Online {
		t.Error("expected online")
	}
	if d.Health.Status != HealthHealthy {
		t.Errorf("health = %s, want healthy", d.Health.Status)
	}
	if len(d.Tags) != 2 {
		t.Errorf("tags = %v, want 2 items", d.Tags)
	}
	if d.Tags[0] != "managed-servers" {
		t.Errorf("tag[0] = %q, want 'managed-servers'", d.Tags[0])
	}

	// Check offline server
	d, ok = sync.GetDevice("offline-server")
	if !ok {
		t.Fatal("offline-server not found")
	}
	if d.Connectivity.Online {
		t.Error("expected offline")
	}
}

func TestHeadscaleSync_SyncUpdatesExisting(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		nodes := HeadscaleNodesResponse{
			Nodes: []HeadscaleNode{
				{
					ID:          "1",
					Name:        "server-01",
					GivenName:   "server-01",
					IPAddresses: []string{"100.64.0.1"},
					Online:      callCount <= 1, // Online first time, offline second
				},
			},
		}
		json.NewEncoder(w).Encode(nodes)
	}))
	defer server.Close()

	sync := NewHeadscaleSync(HeadscaleSyncConfig{
		BaseURL: server.URL,
		APIKey:  "key",
	}, logr.Discard())

	// First sync — online
	sync.Sync(context.Background())
	d, _ := sync.GetDevice("server-01")
	if !d.Connectivity.Online {
		t.Error("expected online after first sync")
	}

	// Second sync — offline
	sync.Sync(context.Background())
	d, _ = sync.GetDevice("server-01")
	if d.Connectivity.Online {
		t.Error("expected offline after second sync")
	}
}

func TestHeadscaleSync_DevicesList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(HeadscaleNodesResponse{
			Nodes: []HeadscaleNode{
				{ID: "1", Name: "a", GivenName: "a", Online: true, IPAddresses: []string{"100.64.0.1"}},
				{ID: "2", Name: "b", GivenName: "b", Online: true, IPAddresses: []string{"100.64.0.2"}},
			},
		})
	}))
	defer server.Close()

	s := NewHeadscaleSync(HeadscaleSyncConfig{BaseURL: server.URL, APIKey: "k"}, logr.Discard())
	s.Sync(context.Background())

	devices := s.Devices()
	if len(devices) != 2 {
		t.Errorf("Devices() len = %d, want 2", len(devices))
	}
}

func TestHeadscaleSync_StatusSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(HeadscaleNodesResponse{
			Nodes: []HeadscaleNode{{ID: "1", Name: "a", GivenName: "a", Online: true, IPAddresses: []string{"100.64.0.1"}}},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	s := NewHeadscaleSync(HeadscaleSyncConfig{BaseURL: server.URL, APIKey: "k"}, logr.Discard())
	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	st := s.Status()
	if !st.Healthy {
		t.Fatalf("Healthy = false, want true")
	}
	if st.TotalSyncs != 1 {
		t.Fatalf("TotalSyncs = %d, want 1", st.TotalSyncs)
	}
	if st.TotalFailures != 0 {
		t.Fatalf("TotalFailures = %d, want 0", st.TotalFailures)
	}
	if st.LastSuccess == nil {
		t.Fatalf("LastSuccess = nil, want timestamp")
	}
	if st.DeviceCount != 1 {
		t.Fatalf("DeviceCount = %d, want 1", st.DeviceCount)
	}
	if st.Stale {
		t.Fatalf("Stale = true, want false")
	}
	if st.FreshnessThreshold == "" {
		t.Fatalf("FreshnessThreshold empty")
	}
}

func TestHeadscaleSync_StatusFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	s := NewHeadscaleSync(HeadscaleSyncConfig{BaseURL: server.URL, APIKey: "bad"}, logr.Discard())
	if err := s.Sync(context.Background()); err == nil {
		t.Fatalf("expected sync error")
	}

	st := s.Status()
	if st.Healthy {
		t.Fatalf("Healthy = true, want false")
	}
	if st.TotalSyncs != 1 {
		t.Fatalf("TotalSyncs = %d, want 1", st.TotalSyncs)
	}
	if st.TotalFailures != 1 {
		t.Fatalf("TotalFailures = %d, want 1", st.TotalFailures)
	}
	if st.ConsecutiveFailures != 1 {
		t.Fatalf("ConsecutiveFailures = %d, want 1", st.ConsecutiveFailures)
	}
	if st.LastAttempt == nil {
		t.Fatalf("LastAttempt = nil, want timestamp")
	}
	if st.LastError == "" {
		t.Fatalf("LastError empty, want message")
	}
	if !st.Stale {
		t.Fatalf("Stale = false, want true")
	}
}
