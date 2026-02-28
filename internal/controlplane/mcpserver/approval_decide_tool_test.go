package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	coreapprovalpolicy "github.com/marcus-qen/legator/internal/controlplane/core/approvalpolicy"
)

func TestHandleDecideApproval_RequiresApprovalID(t *testing.T) {
	srv, _, _, _ := newTestMCPServer(t)
	called := false
	srv.decideApproval = func(string, *coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error) {
		called = true
		return nil, nil
	}

	result, _, err := srv.handleDecideApproval(context.Background(), nil, decideApprovalInput{
		Decision:  "denied",
		DecidedBy: "operator",
	})
	if result != nil {
		t.Fatalf("expected nil tool result, got %#v", result)
	}
	if err == nil || err.Error() != "approval_id is required" {
		t.Fatalf("expected approval_id validation error, got %v", err)
	}
	if called {
		t.Fatal("decide handler should not be called when approval_id is missing")
	}
}

func TestHandleDecideApproval_ParityWithHTTPContracts(t *testing.T) {
	testCases := []struct {
		name             string
		input            decideApprovalInput
		decide           func(id string, request *coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error)
		wantDecideCalls  int
		wantSuccessState string
	}{
		{
			name: "denied_success",
			input: decideApprovalInput{
				ApprovalID: "req-mcp-denied",
				Decision:   "denied",
				DecidedBy:  "operator",
			},
			decide: func(id string, request *coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error) {
				return &coreapprovalpolicy.ApprovalDecisionResult{Request: &approval.Request{ID: id, Decision: request.Decision}}, nil
			},
			wantDecideCalls:  1,
			wantSuccessState: string(approval.DecisionDenied),
		},
		{
			name: "missing_decided_by_decode_error",
			input: decideApprovalInput{
				ApprovalID: "req-mcp-decode",
				Decision:   "denied",
				DecidedBy:  "",
			},
			decide: func(string, *coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error) {
				t.Fatal("decide handler should not be called on decode failure")
				return nil, nil
			},
			wantDecideCalls: 0,
		},
		{
			name: "approved_dispatch_failure_error_mapping",
			input: decideApprovalInput{
				ApprovalID: "req-mcp-dispatch",
				Decision:   "approved",
				DecidedBy:  "operator",
			},
			decide: func(string, *coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error) {
				return nil, &coreapprovalpolicy.ApprovedDispatchError{Err: errors.New("probe probe-mcp not connected")}
			},
			wantDecideCalls: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _, _ := newTestMCPServer(t)
			decideCalls := 0
			srv.decideApproval = func(id string, request *coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error) {
				decideCalls++
				return tc.decide(id, request)
			}

			body, err := json.Marshal(map[string]string{
				"decision":   tc.input.Decision,
				"decided_by": tc.input.DecidedBy,
			})
			if err != nil {
				t.Fatalf("marshal decide body: %v", err)
			}

			httpProjection := coreapprovalpolicy.OrchestrateDecideApproval(bytes.NewReader(body), func(request *coreapprovalpolicy.DecideApprovalRequest) (*coreapprovalpolicy.ApprovalDecisionResult, error) {
				return tc.decide(tc.input.ApprovalID, request)
			}, coreapprovalpolicy.DecideApprovalRenderTargetHTTP)

			result, _, mcpErr := srv.handleDecideApproval(context.Background(), nil, tc.input)

			httpErr, hasHTTPError := httpProjection.HTTPError()
			if hasHTTPError {
				if result != nil {
					t.Fatalf("expected nil tool result on error, got %#v", result)
				}
				if mcpErr == nil {
					t.Fatalf("expected MCP error %q, got nil", httpErr.Message)
				}
				if mcpErr.Error() != httpErr.Message {
					t.Fatalf("expected MCP error %q, got %q", httpErr.Message, mcpErr.Error())
				}
			} else {
				if mcpErr != nil {
					t.Fatalf("unexpected MCP error: %v", mcpErr)
				}
				if result == nil {
					t.Fatal("expected MCP success result")
				}

				var payload coreapprovalpolicy.DecideApprovalSuccess
				if err := json.Unmarshal([]byte(toolText(t, result)), &payload); err != nil {
					t.Fatalf("decode tool payload: %v", err)
				}

				if httpProjection.Success == nil {
					t.Fatalf("expected HTTP success projection, got %+v", httpProjection)
				}
				if payload.Status != httpProjection.Success.Status {
					t.Fatalf("expected MCP status %q to match HTTP status %q", payload.Status, httpProjection.Success.Status)
				}
				if payload.Request == nil || httpProjection.Success.Request == nil {
					t.Fatalf("expected request payloads in both projections, mcp=%+v http=%+v", payload.Request, httpProjection.Success.Request)
				}
				if payload.Request.ID != httpProjection.Success.Request.ID {
					t.Fatalf("expected request id parity, mcp=%q http=%q", payload.Request.ID, httpProjection.Success.Request.ID)
				}
				if payload.Status != tc.wantSuccessState {
					t.Fatalf("expected success status %q, got %q", tc.wantSuccessState, payload.Status)
				}
			}

			if decideCalls != tc.wantDecideCalls {
				t.Fatalf("expected decide call count %d, got %d", tc.wantDecideCalls, decideCalls)
			}
		})
	}
}
