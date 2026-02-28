package grafana

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPClientSnapshotSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"database": "ok", "version": "10.4.1", "commit": "abc123"})
		case r.URL.Path == "/api/datasources":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"uid": "prom", "name": "Prometheus", "type": "prometheus", "isDefault": true, "readOnly": true},
				{"uid": "loki", "name": "Loki", "type": "loki", "isDefault": false, "readOnly": false},
			})
		case r.URL.Path == "/api/search":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"uid": "cap-a"}, {"uid": "cap-b"}})
		case r.URL.Path == "/api/dashboards/uid/cap-a":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"dashboard": map[string]any{
					"uid":   "cap-a",
					"title": "Capacity A",
					"panels": []map[string]any{
						{"datasource": "prom", "targets": []map[string]any{{"expr": "sum(up)"}}},
						{"datasource": map[string]any{"uid": "loki"}, "targets": []map[string]any{{"expr": "count_over_time"}}},
					},
				},
			})
		case r.URL.Path == "/api/dashboards/uid/cap-b":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"dashboard": map[string]any{
					"uid":   "cap-b",
					"title": "Capacity B",
					"panels": []map[string]any{
						{
							"panels": []map[string]any{
								{"datasource": "prom", "targets": []map[string]any{{"expr": "node_memory"}}},
							},
						},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(ClientConfig{BaseURL: srv.URL, APIToken: "token", DashboardLimit: 5, Timeout: 5 * time.Second})
	snapshot, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !snapshot.Health.Healthy {
		t.Fatalf("expected healthy service health: %+v", snapshot.Health)
	}
	if snapshot.Datasources.Total != 2 {
		t.Fatalf("expected 2 datasources, got %d", snapshot.Datasources.Total)
	}
	if snapshot.Dashboards.Total != 2 || snapshot.Dashboards.Scanned != 2 {
		t.Fatalf("unexpected dashboard totals: %+v", snapshot.Dashboards)
	}
	if snapshot.Dashboards.Panels != 4 {
		t.Fatalf("expected 4 flattened panels, got %d", snapshot.Dashboards.Panels)
	}
	if snapshot.Dashboards.QueryBackedPanels != 3 {
		t.Fatalf("expected 3 query-backed panels, got %d", snapshot.Dashboards.QueryBackedPanels)
	}
	if snapshot.Indicators.Availability != "ready" {
		t.Fatalf("expected availability=ready, got %q", snapshot.Indicators.Availability)
	}
	if len(snapshot.DashboardItems) != 2 {
		t.Fatalf("expected 2 dashboard snapshot entries, got %d", len(snapshot.DashboardItems))
	}
}

func TestHTTPClientSnapshotPartialOnDashboardFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"database": "ok", "version": "10.4.1"})
		case "/api/datasources":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"uid": "prom", "name": "Prometheus", "type": "prometheus"}})
		case "/api/search":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"uid": "broken-dash"}})
		case "/api/dashboards/uid/broken-dash":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(ClientConfig{BaseURL: srv.URL, DashboardLimit: 3})
	snapshot, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !snapshot.Partial {
		t.Fatal("expected partial snapshot")
	}
	if len(snapshot.Warnings) == 0 {
		t.Fatal("expected warnings for failed dashboard fetch")
	}
	if snapshot.Dashboards.Scanned != 0 {
		t.Fatalf("expected zero scanned dashboards after fetch failures, got %d", snapshot.Dashboards.Scanned)
	}
}

func TestHTTPClientStatusReflectsSnapshotState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"database": "ok"})
		case "/api/datasources":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"uid": "prom", "name": "Prometheus", "type": "prometheus"}})
		case "/api/search":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := NewHTTPClient(ClientConfig{BaseURL: srv.URL})
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Connected {
		t.Fatal("expected status.connected=true")
	}
	if status.Datasources.Total != 1 {
		t.Fatalf("expected datasource total=1, got %d", status.Datasources.Total)
	}
}

func TestHTTPClientHealthAuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
	}))
	defer srv.Close()

	client := NewHTTPClient(ClientConfig{BaseURL: srv.URL})
	_, err := client.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *ClientError
	if !errorsAs(err, &ce) {
		t.Fatalf("expected ClientError, got %T", err)
	}
	if ce.Code != "auth_failed" {
		t.Fatalf("expected auth_failed, got %q", ce.Code)
	}
}

func TestHTTPClientConfigInvalid(t *testing.T) {
	client := NewHTTPClient(ClientConfig{})
	_, err := client.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("expected config error, got %v", err)
	}
}

func errorsAs(err error, target **ClientError) bool {
	ce, ok := err.(*ClientError)
	if !ok {
		return false
	}
	*target = ce
	return true
}
