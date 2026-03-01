package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/config"
	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
	"github.com/marcus-qen/legator/internal/controlplane/kubeflow"
	"go.uber.org/zap"
)

type fakeKubeflowClient struct {
	statusFn    func() (kubeflow.Status, error)
	inventoryFn func() (kubeflow.Inventory, error)
	refreshFn   func() (kubeflow.RefreshResult, error)
	runStatusFn func(request kubeflow.RunStatusRequest) (kubeflow.RunStatusResult, error)
	submitRunFn func(request kubeflow.SubmitRunRequest) (kubeflow.SubmitRunResult, error)
	cancelRunFn func(request kubeflow.CancelRunRequest) (kubeflow.CancelRunResult, error)

	submitCalls int
	cancelCalls int
}

func (f *fakeKubeflowClient) Status(_ context.Context) (kubeflow.Status, error) {
	if f.statusFn != nil {
		return f.statusFn()
	}
	return kubeflow.Status{Connected: true, Namespace: "kubeflow", CheckedAt: time.Now().UTC()}, nil
}

func (f *fakeKubeflowClient) Inventory(_ context.Context) (kubeflow.Inventory, error) {
	if f.inventoryFn != nil {
		return f.inventoryFn()
	}
	return kubeflow.Inventory{Namespace: "kubeflow", CollectedAt: time.Now().UTC(), Counts: map[string]int{}}, nil
}

func (f *fakeKubeflowClient) Refresh(_ context.Context) (kubeflow.RefreshResult, error) {
	if f.refreshFn != nil {
		return f.refreshFn()
	}
	return kubeflow.RefreshResult{}, nil
}

func (f *fakeKubeflowClient) RunStatus(_ context.Context, request kubeflow.RunStatusRequest) (kubeflow.RunStatusResult, error) {
	if f.runStatusFn != nil {
		return f.runStatusFn(request)
	}
	return kubeflow.RunStatusResult{Kind: kubeflow.DefaultRunResource, Name: request.Name, Namespace: "kubeflow", Status: "Running", ObservedAt: time.Now().UTC()}, nil
}

func (f *fakeKubeflowClient) SubmitRun(_ context.Context, request kubeflow.SubmitRunRequest) (kubeflow.SubmitRunResult, error) {
	f.submitCalls++
	if f.submitRunFn != nil {
		return f.submitRunFn(request)
	}
	return kubeflow.SubmitRunResult{
		Run:         kubeflow.RunStatusResult{Kind: kubeflow.DefaultRunResource, Name: request.Name, Namespace: "kubeflow", Status: "Pending", ObservedAt: time.Now().UTC()},
		Transition:  kubeflow.StatusTransition{Action: "submit", Before: "new", After: "Pending", Changed: true, ObservedAt: time.Now().UTC()},
		SubmittedAt: time.Now().UTC(),
	}, nil
}

func (f *fakeKubeflowClient) CancelRun(_ context.Context, request kubeflow.CancelRunRequest) (kubeflow.CancelRunResult, error) {
	f.cancelCalls++
	if f.cancelRunFn != nil {
		return f.cancelRunFn(request)
	}
	return kubeflow.CancelRunResult{
		Run:        kubeflow.RunStatusResult{Kind: kubeflow.DefaultRunResource, Name: request.Name, Namespace: "kubeflow", Status: "canceled", ObservedAt: time.Now().UTC()},
		Transition: kubeflow.StatusTransition{Action: "cancel", Before: "Running", After: "canceled", Changed: true, ObservedAt: time.Now().UTC()},
		Canceled:   true,
		CanceledAt: time.Now().UTC(),
	}, nil
}

func newKubeflowPolicyServer(t *testing.T, actionsEnabled bool) (*Server, *fakeKubeflowClient) {
	t.Helper()

	t.Setenv("LEGATOR_AUTH", "true")
	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.DataDir = t.TempDir()
	cfg.AuthEnabled = true
	cfg.Kubeflow.Enabled = true
	cfg.Kubeflow.ActionsEnabled = actionsEnabled

	srv, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(srv.Close)

	fakeClient := &fakeKubeflowClient{}
	srv.kubeflowClient = fakeClient
	srv.kubeflowHandlers = kubeflow.NewHandler(fakeClient, kubeflow.HandlerOptions{ActionsEnabled: actionsEnabled})

	return srv, fakeClient
}

func TestKubeflowSubmitRunAllowedByPolicy(t *testing.T) {
	srv, fakeClient := newKubeflowPolicyServer(t, true)
	writeToken := createAPIKey(t, srv, "fleet-write", auth.PermFleetWrite)

	body := `{"name":"run-a","manifest":{"apiVersion":"kubeflow.org/v1","kind":"Run","metadata":{"name":"run-a"}}}`
	rr := makeRequest(t, srv, http.MethodPost, "/api/v1/kubeflow/runs/submit", writeToken, body)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	if fakeClient.submitCalls != 1 {
		t.Fatalf("expected submit call once, got %d", fakeClient.submitCalls)
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if payload["policy_decision"] != "allow" {
		t.Fatalf("expected allow decision, got %#v", payload)
	}
	if payload["status"] != "submit_executed" {
		t.Fatalf("expected submit_executed status, got %#v", payload)
	}
}

func TestKubeflowCancelRunQueuesAndDispatchesOnApproval(t *testing.T) {
	srv, fakeClient := newKubeflowPolicyServer(t, true)
	writeToken := createAPIKey(t, srv, "fleet-write", auth.PermFleetWrite)
	approvalWriteToken := createAPIKey(t, srv, "approval-write", auth.PermApprovalWrite)

	queueResp := makeRequest(t, srv, http.MethodPost, "/api/v1/kubeflow/runs/run-a/cancel", writeToken, "{}")
	if queueResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for queued cancel, got %d body=%s", queueResp.Code, queueResp.Body.String())
	}
	if fakeClient.cancelCalls != 0 {
		t.Fatalf("expected cancel to be queued (no direct execution), got %d calls", fakeClient.cancelCalls)
	}

	var queued map[string]any
	if err := json.Unmarshal(queueResp.Body.Bytes(), &queued); err != nil {
		t.Fatalf("decode queued response: %v", err)
	}
	if queued["policy_decision"] != "queue" {
		t.Fatalf("expected queue decision, got %#v", queued)
	}
	approvalID, _ := queued["approval_id"].(string)
	if approvalID == "" {
		t.Fatalf("expected approval_id in queued response: %#v", queued)
	}

	decideBody := `{"decision":"approved","decided_by":"tester"}`
	decideResp := makeRequest(t, srv, http.MethodPost, "/api/v1/approvals/"+approvalID+"/decide", approvalWriteToken, decideBody)
	if decideResp.Code != http.StatusOK {
		t.Fatalf("expected 200 from approval decision, got %d body=%s", decideResp.Code, decideResp.Body.String())
	}
	if fakeClient.cancelCalls != 1 {
		t.Fatalf("expected approved cancel to execute once, got %d", fakeClient.cancelCalls)
	}
}

func TestKubeflowSubmitRunDeniedByCapacityPolicy(t *testing.T) {
	srv, fakeClient := newKubeflowPolicyServer(t, true)
	writeToken := createAPIKey(t, srv, "fleet-write", auth.PermFleetWrite)

	srv.approvalCore = coreapprovalpolicy.NewService(
		srv.approvalQueue,
		srv.fleetMgr,
		srv.policyStore,
		coreapprovalpolicy.WithCapacitySignalProvider(coreapprovalpolicy.CapacitySignalProviderFunc(func(context.Context) (*coreapprovalpolicy.CapacitySignals, error) {
			return &coreapprovalpolicy.CapacitySignals{Source: "test", Availability: "degraded", DatasourceCount: 1}, nil
		})),
	)

	body := `{"name":"run-b","manifest":{"apiVersion":"kubeflow.org/v1","kind":"Run","metadata":{"name":"run-b"}}}`
	rr := makeRequest(t, srv, http.MethodPost, "/api/v1/kubeflow/runs/submit", writeToken, body)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 denied response, got %d body=%s", rr.Code, rr.Body.String())
	}
	if fakeClient.submitCalls != 0 {
		t.Fatalf("expected denied submit not to execute, got %d calls", fakeClient.submitCalls)
	}
}

func TestKubeflowMutationsDisabledByDefault(t *testing.T) {
	srv, _ := newKubeflowPolicyServer(t, false)
	writeToken := createAPIKey(t, srv, "fleet-write", auth.PermFleetWrite)

	rr := makeRequest(t, srv, http.MethodPost, "/api/v1/kubeflow/runs/run-a/cancel", writeToken, "{}")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when actions disabled, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestKubeflowRunStatusRoute(t *testing.T) {
	srv, _ := newKubeflowPolicyServer(t, true)
	readToken := createAPIKey(t, srv, "fleet-read", auth.PermFleetRead)

	rr := makeRequest(t, srv, http.MethodGet, "/api/v1/kubeflow/runs/run-a/status", readToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"run"`) {
		t.Fatalf("expected run payload, body=%s", rr.Body.String())
	}
}
