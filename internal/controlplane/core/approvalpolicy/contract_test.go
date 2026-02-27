package approvalpolicy

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
)

func TestDecodeDecideApprovalTransport(t *testing.T) {
	t.Run("invalid json", func(t *testing.T) {
		contract := DecodeDecideApprovalTransport(strings.NewReader("{"))
		if contract == nil {
			t.Fatal("expected transport contract")
		}
		if contract.Request != nil {
			t.Fatalf("expected nil request, got %+v", contract.Request)
		}
		httpErr, ok := contract.HTTPError()
		if !ok {
			t.Fatal("expected HTTP error contract")
		}
		if httpErr.Status != http.StatusBadRequest || httpErr.Code != "invalid_request" || httpErr.Message != "invalid request body" {
			t.Fatalf("unexpected HTTP contract: %+v", httpErr)
		}
	})

	t.Run("missing fields", func(t *testing.T) {
		contract := DecodeDecideApprovalTransport(strings.NewReader(`{"decision":"denied"}`))
		if contract == nil {
			t.Fatal("expected transport contract")
		}
		if contract.Request != nil {
			t.Fatalf("expected nil request, got %+v", contract.Request)
		}
		httpErr, ok := contract.HTTPError()
		if !ok {
			t.Fatal("expected HTTP error contract")
		}
		if httpErr.Status != http.StatusBadRequest || httpErr.Code != "invalid_request" || httpErr.Message != "decision and decided_by are required" {
			t.Fatalf("unexpected HTTP contract: %+v", httpErr)
		}
	})

	t.Run("valid payload", func(t *testing.T) {
		contract := DecodeDecideApprovalTransport(strings.NewReader(`{"decision":"approved","decided_by":"operator"}`))
		if contract == nil {
			t.Fatal("expected transport contract")
		}
		if httpErr, ok := contract.HTTPError(); ok {
			t.Fatalf("unexpected HTTP error contract: %+v", httpErr)
		}
		if contract.Request == nil {
			t.Fatal("expected decoded request")
		}
		if contract.Request.Decision != approval.DecisionApproved {
			t.Fatalf("expected approved decision, got %q", contract.Request.Decision)
		}
		if contract.Request.DecidedBy != "operator" {
			t.Fatalf("expected decided_by=operator, got %q", contract.Request.DecidedBy)
		}
	})
}

func TestEncodeDecideApprovalTransport(t *testing.T) {
	req := &approval.Request{ID: "req-1", Decision: approval.DecisionDenied}
	contract := EncodeDecideApprovalTransport(&ApprovalDecisionResult{Request: req}, nil)
	if contract == nil {
		t.Fatal("expected transport contract")
	}
	if httpErr, ok := contract.HTTPError(); ok {
		t.Fatalf("unexpected HTTP error contract: %+v", httpErr)
	}
	if contract.Success == nil {
		t.Fatal("expected success contract")
	}
	if contract.Success.Status != string(approval.DecisionDenied) {
		t.Fatalf("expected denied status, got %q", contract.Success.Status)
	}
	if contract.Success.Request != req {
		t.Fatalf("expected request passthrough, got %+v", contract.Success.Request)
	}

	empty := EncodeDecideApprovalTransport(nil, nil)
	if empty == nil || empty.Success == nil {
		t.Fatalf("expected empty success contract, got %+v", empty)
	}
	if empty.Success.Status != "" || empty.Success.Request != nil {
		t.Fatalf("expected zero-value success payload, got %+v", empty.Success)
	}
}

func TestAdaptDecideApprovalTransportParity(t *testing.T) {
	approvedReq := &approval.Request{ID: "req-approved", Decision: approval.DecisionApproved}
	deniedReq := &approval.Request{ID: "req-denied", Decision: approval.DecisionDenied}

	tests := []struct {
		name          string
		body          string
		decide        func(*DecideApprovalRequest) (*ApprovalDecisionResult, error)
		wantHTTPError *HTTPErrorContract
		wantStatus    string
		wantRequestID string
	}{
		{
			name: "invalid body",
			body: "{",
			decide: func(*DecideApprovalRequest) (*ApprovalDecisionResult, error) {
				t.Fatal("decide handler should not be called on decode failure")
				return nil, nil
			},
			wantHTTPError: &HTTPErrorContract{Status: http.StatusBadRequest, Code: "invalid_request", Message: "invalid request body"},
		},
		{
			name: "missing required fields",
			body: `{"decision":"denied"}`,
			decide: func(*DecideApprovalRequest) (*ApprovalDecisionResult, error) {
				t.Fatal("decide handler should not be called on decode failure")
				return nil, nil
			},
			wantHTTPError: &HTTPErrorContract{Status: http.StatusBadRequest, Code: "invalid_request", Message: "decision and decided_by are required"},
		},
		{
			name: "invalid decision error maps to bad request",
			body: `{"decision":"maybe","decided_by":"operator"}`,
			decide: func(*DecideApprovalRequest) (*ApprovalDecisionResult, error) {
				return nil, errors.New(`invalid decision "maybe": must be approved or denied`)
			},
			wantHTTPError: &HTTPErrorContract{Status: http.StatusBadRequest, Code: "invalid_request", Message: `invalid decision "maybe": must be approved or denied`},
		},
		{
			name: "approved dispatch failure maps to bad gateway",
			body: `{"decision":"approved","decided_by":"operator"}`,
			decide: func(*DecideApprovalRequest) (*ApprovalDecisionResult, error) {
				return nil, &ApprovedDispatchError{Err: errors.New("probe p1 not connected")}
			},
			wantHTTPError: &HTTPErrorContract{Status: http.StatusBadGateway, Code: "bad_gateway", Message: "approved but dispatch failed: probe p1 not connected"},
		},
		{
			name: "decision hook failure maps to internal error",
			body: `{"decision":"approved","decided_by":"operator"}`,
			decide: func(*DecideApprovalRequest) (*ApprovalDecisionResult, error) {
				return nil, &DecisionHookError{Stage: DecisionHookStageDecisionRecorded, Err: errors.New("audit down")}
			},
			wantHTTPError: &HTTPErrorContract{Status: http.StatusInternalServerError, Code: "internal_error", Message: "audit down"},
		},
		{
			name: "denied success contract",
			body: `{"decision":"denied","decided_by":"operator"}`,
			decide: func(*DecideApprovalRequest) (*ApprovalDecisionResult, error) {
				return &ApprovalDecisionResult{Request: deniedReq}, nil
			},
			wantStatus:    string(approval.DecisionDenied),
			wantRequestID: deniedReq.ID,
		},
		{
			name: "approved success contract",
			body: `{"decision":"approved","decided_by":"operator"}`,
			decide: func(*DecideApprovalRequest) (*ApprovalDecisionResult, error) {
				return &ApprovalDecisionResult{Request: approvedReq}, nil
			},
			wantStatus:    string(approval.DecisionApproved),
			wantRequestID: approvedReq.ID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract := AdaptDecideApprovalTransport(strings.NewReader(tt.body), tt.decide)
			if contract == nil {
				t.Fatal("expected transport contract")
			}

			httpErr, hasHTTPError := contract.HTTPError()
			if tt.wantHTTPError != nil {
				if !hasHTTPError {
					t.Fatalf("expected HTTP error contract, got %+v", contract)
				}
				if *httpErr != *tt.wantHTTPError {
					t.Fatalf("unexpected HTTP error contract: got %+v want %+v", httpErr, tt.wantHTTPError)
				}
				return
			}

			if hasHTTPError {
				t.Fatalf("unexpected HTTP error contract: %+v", httpErr)
			}
			if contract.Success == nil {
				t.Fatalf("expected success contract, got %+v", contract)
			}
			if contract.Success.Status != tt.wantStatus {
				t.Fatalf("unexpected success status: got %q want %q", contract.Success.Status, tt.wantStatus)
			}
			if contract.Success.Request == nil {
				t.Fatalf("expected success request payload, got %+v", contract.Success)
			}
			if contract.Success.Request.ID != tt.wantRequestID {
				t.Fatalf("unexpected request id: got %q want %q", contract.Success.Request.ID, tt.wantRequestID)
			}
		})
	}
}

func TestLegacyDecideApprovalContractWrappers(t *testing.T) {
	decoded, httpErr := DecodeDecideApprovalRequest(strings.NewReader(`{"decision":"approved","decided_by":"operator"}`))
	if httpErr != nil {
		t.Fatalf("unexpected decode error: %+v", httpErr)
	}
	if decoded == nil || decoded.Decision != approval.DecisionApproved || decoded.DecidedBy != "operator" {
		t.Fatalf("unexpected decoded request: %+v", decoded)
	}

	success := EncodeDecideApprovalSuccess(&ApprovalDecisionResult{Request: &approval.Request{ID: "req-legacy", Decision: approval.DecisionDenied}})
	if success == nil || success.Status != string(approval.DecisionDenied) || success.Request == nil || success.Request.ID != "req-legacy" {
		t.Fatalf("unexpected success contract: %+v", success)
	}

	httpErr, ok := DecideApprovalHTTPError(&ApprovedDispatchError{Err: errors.New("not connected")})
	if !ok {
		t.Fatal("expected error mapping")
	}
	if httpErr.Status != http.StatusBadGateway || httpErr.Code != "bad_gateway" || httpErr.Message != "approved but dispatch failed: not connected" {
		t.Fatalf("unexpected error mapping: %+v", httpErr)
	}
}
