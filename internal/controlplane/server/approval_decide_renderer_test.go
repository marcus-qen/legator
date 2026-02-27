package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
)

func TestRenderDecideApprovalHTTP_ParitySuccess(t *testing.T) {
	req := &approval.Request{ID: "req-render-success", Decision: approval.DecisionDenied}
	projection := coreapprovalpolicy.ProjectDecideApprovalTransport(coreapprovalpolicy.EncodeDecideApprovalTransport(&coreapprovalpolicy.ApprovalDecisionResult{Request: req}, nil))

	rr := httptest.NewRecorder()
	renderDecideApprovalHTTP(rr, projection)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json content type, got %q", got)
	}

	var payload map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode success response: %v", err)
	}
	if len(payload) != 2 {
		t.Fatalf("expected exactly {status,request}, got %#v", payload)
	}
	if payload["status"] != string(approval.DecisionDenied) {
		t.Fatalf("expected denied status, got %#v", payload["status"])
	}
	request, ok := payload["request"].(map[string]any)
	if !ok {
		t.Fatalf("expected object request payload, got %#v", payload["request"])
	}
	if request["id"] != req.ID {
		t.Fatalf("expected request id %q, got %#v", req.ID, request["id"])
	}
	if request["decision"] != string(approval.DecisionDenied) {
		t.Fatalf("expected request decision denied, got %#v", request["decision"])
	}
}

func TestRenderDecideApprovalHTTP_ParityError(t *testing.T) {
	projection := coreapprovalpolicy.ProjectDecideApprovalTransport(coreapprovalpolicy.EncodeDecideApprovalTransport(nil, &coreapprovalpolicy.ApprovedDispatchError{Err: errors.New("probe probe-render-error not connected")}))

	rr := httptest.NewRecorder()
	renderDecideApprovalHTTP(rr, projection)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected application/json content type, got %q", got)
	}

	var apiErr APIError
	if err := json.NewDecoder(rr.Body).Decode(&apiErr); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if apiErr.Code != "bad_gateway" {
		t.Fatalf("expected bad_gateway code, got %q", apiErr.Code)
	}
	if apiErr.Error != "approved but dispatch failed: probe probe-render-error not connected" {
		t.Fatalf("unexpected error message: %q", apiErr.Error)
	}
}

func TestOrchestrateDecideApprovalHTTP_RegistryParityWithDirectTarget(t *testing.T) {
	body := `{"decision":"denied","decided_by":"operator"}`
	decide := func(*coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error) {
		return &coreapprovalpolicy.ApprovalDecisionResult{Request: &approval.Request{ID: "req-http-registry", Decision: approval.DecisionDenied}}, nil
	}

	invokeInput := coreapprovalpolicy.AssembleDecideApprovalInvokeHTTP("req-http-registry", strings.NewReader(body))
	viaRegistry := coreapprovalpolicy.InvokeDecideApproval(invokeInput, func(id string, request *coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error) {
		_ = id
		return decide(request)
	}, coreapprovalpolicy.DecideApprovalRenderSurfaceHTTP)
	direct := coreapprovalpolicy.OrchestrateDecideApproval(strings.NewReader(body), decide, coreapprovalpolicy.DecideApprovalRenderTargetHTTP)

	viaErr, viaHasErr := viaRegistry.HTTPError()
	directErr, directHasErr := direct.HTTPError()
	if viaHasErr != directHasErr {
		t.Fatalf("expected error parity, registry=%v direct=%v", viaHasErr, directHasErr)
	}
	if viaHasErr {
		if *viaErr != *directErr {
			t.Fatalf("expected identical error projection, registry=%+v direct=%+v", viaErr, directErr)
		}
		return
	}

	if viaRegistry.Success == nil || direct.Success == nil {
		t.Fatalf("expected success projections, registry=%+v direct=%+v", viaRegistry, direct)
	}
	if viaRegistry.Success.Status != direct.Success.Status {
		t.Fatalf("expected status parity, registry=%q direct=%q", viaRegistry.Success.Status, direct.Success.Status)
	}
	if viaRegistry.Success.Request == nil || direct.Success.Request == nil {
		t.Fatalf("expected request parity, registry=%+v direct=%+v", viaRegistry.Success, direct.Success)
	}
	if viaRegistry.Success.Request.ID != direct.Success.Request.ID || viaRegistry.Success.Request.Decision != direct.Success.Request.Decision {
		t.Fatalf("expected request id/decision parity, registry=%+v direct=%+v", viaRegistry.Success.Request, direct.Success.Request)
	}
}

func TestRenderDecideApprovalHTTP_DispatchAdapterParityWithLegacyRenderer(t *testing.T) {
	cases := []struct {
		name       string
		projection *coreapprovalpolicy.DecideApprovalProjection
	}{
		{
			name: "success projection",
			projection: coreapprovalpolicy.ProjectDecideApprovalTransport(coreapprovalpolicy.EncodeDecideApprovalTransport(
				&coreapprovalpolicy.ApprovalDecisionResult{Request: &approval.Request{ID: "req-http-legacy", Decision: approval.DecisionDenied}},
				nil,
			)),
		},
		{
			name:       "dispatch failure projection",
			projection: coreapprovalpolicy.ProjectDecideApprovalTransport(coreapprovalpolicy.EncodeDecideApprovalTransport(nil, &coreapprovalpolicy.ApprovedDispatchError{Err: errors.New("probe probe-http-legacy not connected")})),
		},
		{
			name:       "nil projection",
			projection: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legacyRR := httptest.NewRecorder()
			legacyRenderDecideApprovalHTTP(legacyRR, tc.projection)

			adapterRR := httptest.NewRecorder()
			renderDecideApprovalHTTP(adapterRR, tc.projection)

			if adapterRR.Code != legacyRR.Code {
				t.Fatalf("expected status parity, adapter=%d legacy=%d", adapterRR.Code, legacyRR.Code)
			}
			if got, want := adapterRR.Header().Get("Content-Type"), legacyRR.Header().Get("Content-Type"); got != want {
				t.Fatalf("expected content-type parity, adapter=%q legacy=%q", got, want)
			}

			var adapterBody any
			if err := json.NewDecoder(adapterRR.Body).Decode(&adapterBody); err != nil {
				t.Fatalf("decode adapter body: %v", err)
			}
			var legacyBody any
			if err := json.NewDecoder(legacyRR.Body).Decode(&legacyBody); err != nil {
				t.Fatalf("decode legacy body: %v", err)
			}
			if !reflect.DeepEqual(adapterBody, legacyBody) {
				t.Fatalf("expected body parity, adapter=%#v legacy=%#v", adapterBody, legacyBody)
			}
		})
	}
}

func legacyRenderDecideApprovalHTTP(w http.ResponseWriter, projection *coreapprovalpolicy.DecideApprovalProjection) {
	if projection == nil {
		projection = coreapprovalpolicy.ProjectDecideApprovalTransport(nil)
	}
	if httpErr, ok := projection.HTTPError(); ok {
		writeJSONError(w, httpErr.Status, httpErr.Code, httpErr.Message)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(projection.Success)
}
