package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
