package kubeflow

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
	statusFn    func() (Status, error)
	inventoryFn func() (Inventory, error)
	refreshFn   func() (RefreshResult, error)
}

func (f *fakeClient) Status(_ context.Context) (Status, error) {
	if f.statusFn == nil {
		return Status{}, nil
	}
	return f.statusFn()
}

func (f *fakeClient) Inventory(_ context.Context) (Inventory, error) {
	if f.inventoryFn == nil {
		return Inventory{}, nil
	}
	return f.inventoryFn()
}

func (f *fakeClient) Refresh(_ context.Context) (RefreshResult, error) {
	if f.refreshFn == nil {
		return RefreshResult{}, nil
	}
	return f.refreshFn()
}

func TestHandlerStatusSuccess(t *testing.T) {
	h := NewHandler(&fakeClient{statusFn: func() (Status, error) {
		return Status{Connected: true, Namespace: "kubeflow", CheckedAt: time.Now().UTC()}, nil
	}}, HandlerOptions{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kubeflow/status", nil)
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

func TestHandlerInventoryMapsClientError(t *testing.T) {
	h := NewHandler(&fakeClient{inventoryFn: func() (Inventory, error) {
		return Inventory{}, &ClientError{Code: "cluster_unreachable", Message: "kubernetes cluster unreachable", Detail: "dial tcp"}
	}}, HandlerOptions{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kubeflow/inventory", nil)
	rr := httptest.NewRecorder()
	h.HandleInventory(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "cluster_unreachable") {
		t.Fatalf("expected cluster_unreachable code, body=%s", rr.Body.String())
	}
}

func TestHandlerRefreshDisabledByDefault(t *testing.T) {
	h := NewHandler(&fakeClient{}, HandlerOptions{ActionsEnabled: false})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kubeflow/actions/refresh", nil)
	rr := httptest.NewRecorder()
	h.HandleRefresh(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "action_disabled") {
		t.Fatalf("expected action_disabled response, body=%s", rr.Body.String())
	}
}

func TestHandlerRefreshEnabled(t *testing.T) {
	h := NewHandler(&fakeClient{refreshFn: func() (RefreshResult, error) {
		return RefreshResult{Status: Status{Connected: true}, Inventory: Inventory{Namespace: "kubeflow"}}, nil
	}}, HandlerOptions{ActionsEnabled: true})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/kubeflow/actions/refresh", nil)
	rr := httptest.NewRecorder()
	h.HandleRefresh(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "\"refresh\"") {
		t.Fatalf("expected refresh payload, body=%s", rr.Body.String())
	}
}

func TestWriteClientErrorFallback(t *testing.T) {
	rr := httptest.NewRecorder()
	writeClientError(rr, errors.New("boom"))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}
}
