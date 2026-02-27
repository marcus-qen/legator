package approvalpolicy

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
)

func TestDispatchDecideApprovalResponseForSurface_ParityWithProjectionContracts(t *testing.T) {
	tests := []struct {
		name             string
		projection       *DecideApprovalProjection
		wantHTTPError    *HTTPErrorContract
		wantMCPError     string
		wantSuccessState string
		wantRequestID    string
	}{
		{
			name:          "http+mcp error parity",
			projection:    ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(nil, &ApprovedDispatchError{Err: errors.New("probe p-adapter not connected")})),
			wantHTTPError: &HTTPErrorContract{Status: http.StatusBadGateway, Code: "bad_gateway", Message: "approved but dispatch failed: probe p-adapter not connected"},
			wantMCPError:  "approved but dispatch failed: probe p-adapter not connected",
		},
		{
			name: "http+mcp success parity",
			projection: ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(
				&ApprovalDecisionResult{Request: &approval.Request{ID: "req-adapter", Decision: approval.DecisionDenied}},
				nil,
			)),
			wantSuccessState: string(approval.DecisionDenied),
			wantRequestID:    "req-adapter",
		},
		{
			name:          "nil projection parity",
			projection:    nil,
			wantHTTPError: &HTTPErrorContract{Status: http.StatusInternalServerError, Code: "internal_error", Message: "approval decide adapter returned empty contract"},
			wantMCPError:  "approval decide adapter returned empty contract",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				httpErr     *HTTPErrorContract
				httpSuccess *DecideApprovalSuccess
				mcpErr      error
				mcpSuccess  *DecideApprovalSuccess
			)

			DispatchDecideApprovalResponseForSurface(tt.projection, DecideApprovalRenderSurfaceHTTP, DecideApprovalResponseDispatchWriter{
				WriteHTTPError: func(err *HTTPErrorContract) {
					httpErr = err
				},
				WriteSuccess: func(success *DecideApprovalSuccess) {
					httpSuccess = success
				},
			})
			DispatchDecideApprovalResponseForSurface(tt.projection, DecideApprovalRenderSurfaceMCP, DecideApprovalResponseDispatchWriter{
				WriteMCPError: func(err error) {
					mcpErr = err
				},
				WriteSuccess: func(success *DecideApprovalSuccess) {
					mcpSuccess = success
				},
			})

			if tt.wantHTTPError != nil {
				if httpErr == nil {
					t.Fatalf("expected HTTP error projection %+v, got nil", tt.wantHTTPError)
				}
				if *httpErr != *tt.wantHTTPError {
					t.Fatalf("unexpected HTTP error projection: got %+v want %+v", httpErr, tt.wantHTTPError)
				}
				if mcpErr == nil {
					t.Fatalf("expected MCP error %q, got nil", tt.wantMCPError)
				}
				if mcpErr.Error() != tt.wantMCPError {
					t.Fatalf("unexpected MCP error message: got %q want %q", mcpErr.Error(), tt.wantMCPError)
				}
				if httpSuccess != nil || mcpSuccess != nil {
					t.Fatalf("did not expect success callbacks on error path, http=%+v mcp=%+v", httpSuccess, mcpSuccess)
				}
				return
			}

			if httpErr != nil || mcpErr != nil {
				t.Fatalf("unexpected errors for success path, http=%+v mcp=%v", httpErr, mcpErr)
			}
			if httpSuccess == nil || mcpSuccess == nil {
				t.Fatalf("expected success callbacks, http=%+v mcp=%+v", httpSuccess, mcpSuccess)
			}
			if httpSuccess.Status != tt.wantSuccessState || mcpSuccess.Status != tt.wantSuccessState {
				t.Fatalf("unexpected success status parity, http=%q mcp=%q want=%q", httpSuccess.Status, mcpSuccess.Status, tt.wantSuccessState)
			}
			if httpSuccess.Request == nil || mcpSuccess.Request == nil {
				t.Fatalf("expected request payload parity, http=%+v mcp=%+v", httpSuccess, mcpSuccess)
			}
			if httpSuccess.Request.ID != tt.wantRequestID || mcpSuccess.Request.ID != tt.wantRequestID {
				t.Fatalf("unexpected request id parity, http=%q mcp=%q want=%q", httpSuccess.Request.ID, mcpSuccess.Request.ID, tt.wantRequestID)
			}
		})
	}
}

func TestDispatchDecideApprovalResponseForSurface_UnsupportedSurfaceFallback(t *testing.T) {
	var (
		httpErr *HTTPErrorContract
		mcpErr  error
	)

	DispatchDecideApprovalResponseForSurface(ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(&ApprovalDecisionResult{}, nil)), DecideApprovalRenderSurface("bogus"), DecideApprovalResponseDispatchWriter{
		WriteHTTPError: func(err *HTTPErrorContract) {
			httpErr = err
		},
	})
	if httpErr == nil {
		t.Fatal("expected unsupported-surface HTTP error")
	}
	if httpErr.Status != http.StatusInternalServerError || httpErr.Code != "internal_error" {
		t.Fatalf("unexpected unsupported-surface HTTP error: %+v", httpErr)
	}
	if !strings.Contains(httpErr.Message, "unsupported approval decide dispatch surface") {
		t.Fatalf("unexpected unsupported-surface HTTP message: %q", httpErr.Message)
	}

	DispatchDecideApprovalResponseForSurface(ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(&ApprovalDecisionResult{}, nil)), DecideApprovalRenderSurface("bogus"), DecideApprovalResponseDispatchWriter{
		WriteMCPError: func(err error) {
			mcpErr = err
		},
	})
	if mcpErr == nil {
		t.Fatal("expected unsupported-surface MCP error")
	}
	if !strings.Contains(mcpErr.Error(), "unsupported approval decide dispatch surface") {
		t.Fatalf("unexpected unsupported-surface MCP message: %q", mcpErr.Error())
	}
}
