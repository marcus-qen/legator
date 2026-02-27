package mcpserver

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
)

func TestRenderDecideApprovalMCP_ParitySuccess(t *testing.T) {
	projection := orchestrateDecideApprovalMCP(strings.NewReader(`{"decision":"denied","decided_by":"operator"}`), func(*coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error) {
		return &coreapprovalpolicy.ApprovalDecisionResult{Request: &approval.Request{ID: "req-render-success", Decision: approval.DecisionDenied}}, nil
	})

	result, _, err := renderDecideApprovalMCP(projection)
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
	if request["id"] != "req-render-success" {
		t.Fatalf("expected request id req-render-success, got %#v", request["id"])
	}
	if request["decision"] != string(approval.DecisionDenied) {
		t.Fatalf("expected request decision denied, got %#v", request["decision"])
	}
}

func TestRenderDecideApprovalMCP_ParityError(t *testing.T) {
	projection := orchestrateDecideApprovalMCP(strings.NewReader(`{"decision":"approved","decided_by":"operator"}`), func(*coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error) {
		return nil, &coreapprovalpolicy.ApprovedDispatchError{Err: errors.New("probe probe-render-error not connected")}
	})

	result, _, err := renderDecideApprovalMCP(projection)
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

func TestOrchestrateDecideApprovalMCP_ParityWithHTTPProjection(t *testing.T) {
	body := `{"decision":"denied","decided_by":"operator"}`
	decide := func(*coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error) {
		return &coreapprovalpolicy.ApprovalDecisionResult{Request: &approval.Request{ID: "req-cross-parity", Decision: approval.DecisionDenied}}, nil
	}

	httpProjection := coreapprovalpolicy.OrchestrateDecideApproval(strings.NewReader(body), decide, coreapprovalpolicy.DecideApprovalRenderTargetHTTP)
	mcpProjection := orchestrateDecideApprovalMCP(strings.NewReader(body), decide)

	httpErr, httpHasErr := httpProjection.HTTPError()
	mcpErr, mcpHasErr := mcpProjection.HTTPError()
	if httpHasErr != mcpHasErr {
		t.Fatalf("expected parity on error presence, http=%v mcp=%v", httpHasErr, mcpHasErr)
	}
	if httpHasErr && *httpErr != *mcpErr {
		t.Fatalf("expected identical error projection, http=%+v mcp=%+v", httpErr, mcpErr)
	}
	if !httpHasErr {
		if httpProjection.Success == nil || mcpProjection.Success == nil {
			t.Fatalf("expected success projections for both transports, http=%+v mcp=%+v", httpProjection, mcpProjection)
		}
		if httpProjection.Success.Status != mcpProjection.Success.Status {
			t.Fatalf("expected status parity, http=%q mcp=%q", httpProjection.Success.Status, mcpProjection.Success.Status)
		}
		if httpProjection.Success.Request == nil || mcpProjection.Success.Request == nil {
			t.Fatalf("expected request payload parity, http=%+v mcp=%+v", httpProjection.Success, mcpProjection.Success)
		}
		if httpProjection.Success.Request.ID != mcpProjection.Success.Request.ID {
			t.Fatalf("expected request id parity, http=%q mcp=%q", httpProjection.Success.Request.ID, mcpProjection.Success.Request.ID)
		}
	}
}
