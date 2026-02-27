package mcpserver

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
)

func TestRenderDecideApprovalMCP_ParitySuccess(t *testing.T) {
	req := &approval.Request{ID: "req-render-success", Decision: approval.DecisionDenied}
	contract := coreapprovalpolicy.EncodeDecideApprovalTransport(&coreapprovalpolicy.ApprovalDecisionResult{Request: req}, nil)

	result, _, err := renderDecideApprovalMCP(contract)
	if err != nil {
		t.Fatalf("renderDecideApprovalMCP returned error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(toolText(t, result)), &payload); err != nil {
		t.Fatalf("decode success payload: %v", err)
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

func TestRenderDecideApprovalMCP_ParityError(t *testing.T) {
	contract := coreapprovalpolicy.EncodeDecideApprovalTransport(nil, &coreapprovalpolicy.ApprovedDispatchError{Err: errors.New("probe probe-render-error not connected")})

	result, _, err := renderDecideApprovalMCP(contract)
	if result != nil {
		t.Fatalf("expected nil result on error, got %#v", result)
	}
	if err == nil {
		t.Fatal("expected decide render error")
	}
	if err.Error() != "approved but dispatch failed: probe probe-render-error not connected" {
		t.Fatalf("unexpected error message: %q", err.Error())
	}
}
