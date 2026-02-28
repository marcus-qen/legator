package grafana

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeClient struct {
	statusFn   func() (Status, error)
	snapshotFn func() (Snapshot, error)
}

func (f *fakeClient) Status(_ context.Context) (Status, error) {
	if f.statusFn == nil {
		return Status{}, nil
	}
	return f.statusFn()
}

func (f *fakeClient) Snapshot(_ context.Context) (Snapshot, error) {
	if f.snapshotFn == nil {
		return Snapshot{}, nil
	}
	return f.snapshotFn()
}

func TestHandlerStatusSuccess(t *testing.T) {
	h := NewHandler(&fakeClient{statusFn: func() (Status, error) {
		return Status{Connected: true, CheckedAt: time.Now().UTC()}, nil
	}})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/grafana/status", nil)
	rr := httptest.NewRecorder()
	h.HandleStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var payload map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := payload["status"]; !ok {
		t.Fatalf("expected status field, got %v", payload)
	}
}

func TestHandlerSnapshotMapsClientError(t *testing.T) {
	h := NewHandler(&fakeClient{snapshotFn: func() (Snapshot, error) {
		return Snapshot{}, &ClientError{Code: "auth_failed", Message: "grafana authentication failed", Detail: "401"}
	}})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/grafana/snapshot", nil)
	rr := httptest.NewRecorder()
	h.HandleSnapshot(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "auth_failed") {
		t.Fatalf("expected auth_failed code, body=%s", rr.Body.String())
	}
}

func TestHandlerStatusConfigInvalid(t *testing.T) {
	h := NewHandler(&fakeClient{statusFn: func() (Status, error) {
		return Status{}, &ClientError{Code: "config_invalid", Message: "grafana base URL is not configured"}
	}})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/grafana/status", nil)
	rr := httptest.NewRecorder()
	h.HandleStatus(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}

func TestWriteClientErrorFallback(t *testing.T) {
	rr := httptest.NewRecorder()
	writeClientError(rr, errors.New("boom"))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}
}
