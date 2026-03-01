package kubeflow

import (
	"bytes"
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
	runStatusFn func(request RunStatusRequest) (RunStatusResult, error)
	submitRunFn func(request SubmitRunRequest) (SubmitRunResult, error)
	cancelRunFn func(request CancelRunRequest) (CancelRunResult, error)
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

func (f *fakeClient) RunStatus(_ context.Context, request RunStatusRequest) (RunStatusResult, error) {
	if f.runStatusFn == nil {
		return RunStatusResult{}, nil
	}
	return f.runStatusFn(request)
}

func (f *fakeClient) SubmitRun(_ context.Context, request SubmitRunRequest) (SubmitRunResult, error) {
	if f.submitRunFn == nil {
		return SubmitRunResult{}, nil
	}
	return f.submitRunFn(request)
}

func (f *fakeClient) CancelRun(_ context.Context, request CancelRunRequest) (CancelRunResult, error) {
	if f.cancelRunFn == nil {
		return CancelRunResult{}, nil
	}
	return f.cancelRunFn(request)
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

func TestHandlerRunStatusSuccess(t *testing.T) {
	h := NewHandler(&fakeClient{runStatusFn: func(request RunStatusRequest) (RunStatusResult, error) {
		if request.Name != "run-a" {
			t.Fatalf("unexpected run name: %s", request.Name)
		}
		return RunStatusResult{Kind: DefaultRunResource, Name: request.Name, Namespace: "kubeflow", Status: "Running"}, nil
	}}, HandlerOptions{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kubeflow/runs/run-a/status?kind=runs.kubeflow.org", nil)
	req.SetPathValue("name", "run-a")
	rr := httptest.NewRecorder()
	h.HandleRunStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "\"run\"") {
		t.Fatalf("expected run payload, body=%s", rr.Body.String())
	}
}

func TestHandlerSubmitRunDisabledByDefault(t *testing.T) {
	h := NewHandler(&fakeClient{}, HandlerOptions{ActionsEnabled: false})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kubeflow/runs/submit", bytes.NewReader([]byte(`{"manifest":{}}`)))
	rr := httptest.NewRecorder()
	h.HandleSubmitRun(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

func TestHandlerSubmitRunEnabled(t *testing.T) {
	h := NewHandler(&fakeClient{submitRunFn: func(request SubmitRunRequest) (SubmitRunResult, error) {
		if request.Name != "run-a" {
			t.Fatalf("expected run-a, got %s", request.Name)
		}
		return SubmitRunResult{
			Run:         RunStatusResult{Kind: DefaultRunResource, Name: "run-a", Namespace: "kubeflow", Status: "Pending"},
			Transition:  StatusTransition{Action: "submit", Before: "new", After: "Pending", Changed: true, ObservedAt: time.Now().UTC()},
			SubmittedAt: time.Now().UTC(),
		}, nil
	}}, HandlerOptions{ActionsEnabled: true})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/kubeflow/runs/submit?name=run-a", bytes.NewReader([]byte(`{"manifest":{"apiVersion":"v1"}}`)))
	rr := httptest.NewRecorder()
	h.HandleSubmitRun(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "\"submit\"") {
		t.Fatalf("expected submit payload, body=%s", rr.Body.String())
	}
}

func TestHandlerCancelRunEnabled(t *testing.T) {
	h := NewHandler(&fakeClient{cancelRunFn: func(request CancelRunRequest) (CancelRunResult, error) {
		if request.Name != "run-a" {
			t.Fatalf("unexpected run name: %s", request.Name)
		}
		return CancelRunResult{
			Run:        RunStatusResult{Kind: DefaultRunResource, Name: "run-a", Namespace: "kubeflow", Status: "canceled"},
			Transition: StatusTransition{Action: "cancel", Before: "Running", After: "canceled", Changed: true, ObservedAt: time.Now().UTC()},
			Canceled:   true,
			CanceledAt: time.Now().UTC(),
		}, nil
	}}, HandlerOptions{ActionsEnabled: true})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/kubeflow/runs/run-a/cancel", nil)
	req.SetPathValue("name", "run-a")
	rr := httptest.NewRecorder()
	h.HandleCancelRun(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "\"cancel\"") {
		t.Fatalf("expected cancel payload, body=%s", rr.Body.String())
	}
}

func TestWriteClientErrorFallback(t *testing.T) {
	rr := httptest.NewRecorder()
	writeClientError(rr, errors.New("boom"))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}
}

func TestWriteClientErrorInvalidRequest(t *testing.T) {
	rr := httptest.NewRecorder()
	writeClientError(rr, &ClientError{Code: "invalid_request", Message: "manifest invalid"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}
