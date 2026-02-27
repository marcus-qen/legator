package approvalpolicy

import (
	"net/http"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
)

func TestAssembleDecideApprovalInvokeMCP_NormalizesApprovalIDAndBody(t *testing.T) {
	invokeInput, err := AssembleDecideApprovalInvokeMCP("  req-mcp-normalized  ", "denied", "operator")
	if err != nil {
		t.Fatalf("AssembleDecideApprovalInvokeMCP returned error: %v", err)
	}
	if invokeInput == nil {
		t.Fatal("expected invoke input")
	}
	if invokeInput.ApprovalID != "req-mcp-normalized" {
		t.Fatalf("expected trimmed approval id, got %q", invokeInput.ApprovalID)
	}

	decoded := DecodeDecideApprovalTransport(invokeInput.Body)
	if decoded == nil {
		t.Fatal("expected decoded transport contract")
	}
	if httpErr, ok := decoded.HTTPError(); ok {
		t.Fatalf("unexpected decode error from assembled MCP body: %+v", httpErr)
	}
	if decoded.Request == nil {
		t.Fatalf("expected decoded request, got %+v", decoded)
	}
	if decoded.Request.Decision != approval.DecisionDenied {
		t.Fatalf("expected denied decision, got %q", decoded.Request.Decision)
	}
	if decoded.Request.DecidedBy != "operator" {
		t.Fatalf("expected decided_by operator, got %q", decoded.Request.DecidedBy)
	}
}

func TestAssembleDecideApprovalInvokeMCP_RequiresApprovalID(t *testing.T) {
	invokeInput, err := AssembleDecideApprovalInvokeMCP("   ", "approved", "operator")
	if invokeInput != nil {
		t.Fatalf("expected nil invoke input on missing approval id, got %+v", invokeInput)
	}
	if err == nil || err.Error() != "approval_id is required" {
		t.Fatalf("expected approval_id validation error, got %v", err)
	}
}

func TestInvokeDecideApproval_ParityAcrossHTTPAndMCPInputs(t *testing.T) {
	httpInput := AssembleDecideApprovalInvokeHTTP("req-invoke-parity", strings.NewReader(`{"decision":"approved","decided_by":"operator"}`))
	mcpInput, err := AssembleDecideApprovalInvokeMCP("  req-invoke-parity  ", "approved", "operator")
	if err != nil {
		t.Fatalf("AssembleDecideApprovalInvokeMCP returned error: %v", err)
	}

	var httpCalledID string
	var httpCalledRequest *DecideApprovalRequest
	httpProjection := InvokeDecideApproval(httpInput, func(id string, request *DecideApprovalRequest) (*ApprovalDecisionResult, error) {
		httpCalledID = id
		httpCalledRequest = request
		return &ApprovalDecisionResult{Request: &approval.Request{ID: id, Decision: request.Decision}}, nil
	}, DecideApprovalRenderSurfaceHTTP)

	var mcpCalledID string
	var mcpCalledRequest *DecideApprovalRequest
	mcpProjection := InvokeDecideApproval(mcpInput, func(id string, request *DecideApprovalRequest) (*ApprovalDecisionResult, error) {
		mcpCalledID = id
		mcpCalledRequest = request
		return &ApprovalDecisionResult{Request: &approval.Request{ID: id, Decision: request.Decision}}, nil
	}, DecideApprovalRenderSurfaceMCP)

	if httpCalledID != "req-invoke-parity" || mcpCalledID != "req-invoke-parity" {
		t.Fatalf("expected identical normalized invoke ids, http=%q mcp=%q", httpCalledID, mcpCalledID)
	}
	if httpCalledRequest == nil || mcpCalledRequest == nil {
		t.Fatalf("expected invoke request payloads for both paths, http=%+v mcp=%+v", httpCalledRequest, mcpCalledRequest)
	}
	if httpCalledRequest.Decision != mcpCalledRequest.Decision || httpCalledRequest.DecidedBy != mcpCalledRequest.DecidedBy {
		t.Fatalf("expected invoke request parity, http=%+v mcp=%+v", httpCalledRequest, mcpCalledRequest)
	}

	httpErr, httpHasErr := httpProjection.HTTPError()
	mcpErr, mcpHasErr := mcpProjection.HTTPError()
	if httpHasErr || mcpHasErr {
		t.Fatalf("expected success projections, httpErr=%+v mcpErr=%+v", httpErr, mcpErr)
	}
	if httpProjection.Success == nil || mcpProjection.Success == nil {
		t.Fatalf("expected success payloads, http=%+v mcp=%+v", httpProjection, mcpProjection)
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

func TestInvokeDecideApproval_BodyDecodeParityAcrossHTTPAndMCPInputs(t *testing.T) {
	httpInput := AssembleDecideApprovalInvokeHTTP("req-body-parity", strings.NewReader(`{"decision":"denied"}`))
	mcpInput, err := AssembleDecideApprovalInvokeMCP("req-body-parity", "denied", "")
	if err != nil {
		t.Fatalf("AssembleDecideApprovalInvokeMCP returned error: %v", err)
	}

	httpCalls := 0
	httpProjection := InvokeDecideApproval(httpInput, func(string, *DecideApprovalRequest) (*ApprovalDecisionResult, error) {
		httpCalls++
		return nil, nil
	}, DecideApprovalRenderSurfaceHTTP)

	mcpCalls := 0
	mcpProjection := InvokeDecideApproval(mcpInput, func(string, *DecideApprovalRequest) (*ApprovalDecisionResult, error) {
		mcpCalls++
		return nil, nil
	}, DecideApprovalRenderSurfaceMCP)

	if httpCalls != 0 || mcpCalls != 0 {
		t.Fatalf("expected decode failure to short-circuit invoke call, http=%d mcp=%d", httpCalls, mcpCalls)
	}

	httpErr, httpHasErr := httpProjection.HTTPError()
	mcpErr, mcpHasErr := mcpProjection.HTTPError()
	if !httpHasErr || !mcpHasErr {
		t.Fatalf("expected decode errors in both projections, http=%+v mcp=%+v", httpProjection, mcpProjection)
	}
	if *httpErr != *mcpErr {
		t.Fatalf("expected identical decode errors, http=%+v mcp=%+v", httpErr, mcpErr)
	}
	if httpErr.Status != http.StatusBadRequest || httpErr.Code != "invalid_request" || httpErr.Message != "decision and decided_by are required" {
		t.Fatalf("unexpected decode error contract: %+v", httpErr)
	}
}
