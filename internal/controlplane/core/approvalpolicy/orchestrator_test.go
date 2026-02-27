package approvalpolicy

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
)

func TestOrchestrateDecideApproval_ParityAcrossTargets(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		decide        func(*DecideApprovalRequest) (*ApprovalDecisionResult, error)
		wantHTTPError *HTTPErrorContract
		wantStatus    string
		wantRequestID string
	}{
		{
			name: "invalid body parity",
			body: "{",
			decide: func(*DecideApprovalRequest) (*ApprovalDecisionResult, error) {
				t.Fatal("decide handler should not be called on decode failure")
				return nil, nil
			},
			wantHTTPError: &HTTPErrorContract{Status: http.StatusBadRequest, Code: "invalid_request", Message: "invalid request body"},
		},
		{
			name: "approved dispatch failure parity",
			body: `{"decision":"approved","decided_by":"operator"}`,
			decide: func(*DecideApprovalRequest) (*ApprovalDecisionResult, error) {
				return nil, &ApprovedDispatchError{Err: errors.New("probe parity-probe not connected")}
			},
			wantHTTPError: &HTTPErrorContract{Status: http.StatusBadGateway, Code: "bad_gateway", Message: "approved but dispatch failed: probe parity-probe not connected"},
		},
		{
			name: "denied success parity",
			body: `{"decision":"denied","decided_by":"operator"}`,
			decide: func(*DecideApprovalRequest) (*ApprovalDecisionResult, error) {
				return &ApprovalDecisionResult{Request: &approval.Request{ID: "req-parity", Decision: approval.DecisionDenied}}, nil
			},
			wantStatus:    string(approval.DecisionDenied),
			wantRequestID: "req-parity",
		},
	}

	targets := []DecideApprovalRenderTarget{DecideApprovalRenderTargetHTTP, DecideApprovalRenderTargetMCP}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, target := range targets {
				t.Run(string(target), func(t *testing.T) {
					projection := OrchestrateDecideApproval(strings.NewReader(tt.body), tt.decide, target)
					if projection == nil {
						t.Fatal("expected decide projection")
					}

					httpErr, hasHTTPError := projection.HTTPError()
					if tt.wantHTTPError != nil {
						if !hasHTTPError {
							t.Fatalf("expected HTTP error projection, got %+v", projection)
						}
						if *httpErr != *tt.wantHTTPError {
							t.Fatalf("unexpected HTTP error projection: got %+v want %+v", httpErr, tt.wantHTTPError)
						}
						return
					}

					if hasHTTPError {
						t.Fatalf("unexpected HTTP error projection: %+v", httpErr)
					}
					if projection.Success == nil {
						t.Fatalf("expected success projection, got %+v", projection)
					}
					if projection.Success.Status != tt.wantStatus {
						t.Fatalf("unexpected success status: got %q want %q", projection.Success.Status, tt.wantStatus)
					}
					if projection.Success.Request == nil {
						t.Fatalf("expected success request payload, got %+v", projection.Success)
					}
					if projection.Success.Request.ID != tt.wantRequestID {
						t.Fatalf("unexpected request id: got %q want %q", projection.Success.Request.ID, tt.wantRequestID)
					}
				})
			}
		})
	}
}

func TestSelectDecideApprovalProjection_UnsupportedTarget(t *testing.T) {
	projection := SelectDecideApprovalProjection(&DecideApprovalTransportContract{}, DecideApprovalRenderTarget("bogus"))
	if projection == nil {
		t.Fatal("expected projection")
	}
	httpErr, ok := projection.HTTPError()
	if !ok {
		t.Fatal("expected HTTP error projection")
	}
	if httpErr.Status != http.StatusInternalServerError || httpErr.Code != "internal_error" {
		t.Fatalf("unexpected unsupported-target error projection: %+v", httpErr)
	}
	if httpErr.Message != `unsupported approval decide render target "bogus"` {
		t.Fatalf("unexpected unsupported-target message: %q", httpErr.Message)
	}
}

func TestResolveDecideApprovalRenderTarget_RegistryParity(t *testing.T) {
	tests := []struct {
		surface DecideApprovalRenderSurface
		want    DecideApprovalRenderTarget
		ok      bool
	}{
		{surface: DecideApprovalRenderSurfaceHTTP, want: DecideApprovalRenderTargetHTTP, ok: true},
		{surface: DecideApprovalRenderSurfaceMCP, want: DecideApprovalRenderTargetMCP, ok: true},
		{surface: DecideApprovalRenderSurface("bogus"), ok: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.surface), func(t *testing.T) {
			got, ok := ResolveDecideApprovalRenderTarget(tt.surface)
			if ok != tt.ok {
				t.Fatalf("unexpected resolve presence: got %v want %v", ok, tt.ok)
			}
			if tt.ok && got != tt.want {
				t.Fatalf("unexpected target resolution: got %q want %q", got, tt.want)
			}
		})
	}
}

func TestOrchestrateDecideApprovalForSurface_ParityWithDirectTarget(t *testing.T) {
	body := `{"decision":"denied","decided_by":"operator"}`
	decide := func(*DecideApprovalRequest) (*ApprovalDecisionResult, error) {
		return &ApprovalDecisionResult{Request: &approval.Request{ID: "req-registry-parity", Decision: approval.DecisionDenied}}, nil
	}

	tests := []struct {
		name    string
		surface DecideApprovalRenderSurface
		target  DecideApprovalRenderTarget
	}{
		{name: "http", surface: DecideApprovalRenderSurfaceHTTP, target: DecideApprovalRenderTargetHTTP},
		{name: "mcp", surface: DecideApprovalRenderSurfaceMCP, target: DecideApprovalRenderTargetMCP},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viaRegistry := OrchestrateDecideApprovalForSurface(strings.NewReader(body), decide, tt.surface)
			direct := OrchestrateDecideApproval(strings.NewReader(body), decide, tt.target)

			registryErr, registryHasErr := viaRegistry.HTTPError()
			directErr, directHasErr := direct.HTTPError()
			if registryHasErr != directHasErr {
				t.Fatalf("expected error parity, registry=%v direct=%v", registryHasErr, directHasErr)
			}
			if registryHasErr {
				if *registryErr != *directErr {
					t.Fatalf("expected identical error projection, registry=%+v direct=%+v", registryErr, directErr)
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
		})
	}
}
