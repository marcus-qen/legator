package approvalpolicy

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
)

func TestNewDecideApprovalRenderTargetRegistry_ParityWithLegacyInlineSetup(t *testing.T) {
	targets := map[DecideApprovalRenderSurface]DecideApprovalRenderTarget{
		DecideApprovalRenderSurfaceHTTP: DecideApprovalRenderTargetHTTP,
		DecideApprovalRenderSurfaceMCP:  DecideApprovalRenderTargetMCP,
	}

	newRegistry := newDecideApprovalRenderTargetRegistry(targets)
	legacyRegistry := projectiondispatch.NewPolicyRegistry(targets)

	targets[DecideApprovalRenderSurfaceHTTP] = DecideApprovalRenderTarget("mutated")

	tests := []DecideApprovalRenderSurface{
		DecideApprovalRenderSurfaceHTTP,
		DecideApprovalRenderSurfaceMCP,
		DecideApprovalRenderSurface("bogus"),
	}

	for _, surface := range tests {
		t.Run(string(surface), func(t *testing.T) {
			newTarget, newOK := newRegistry.Resolve(surface)
			legacyTarget, legacyOK := legacyRegistry.Resolve(surface)
			if newOK != legacyOK {
				t.Fatalf("resolve presence parity mismatch for %q: new=%v legacy=%v", surface, newOK, legacyOK)
			}
			if newTarget != legacyTarget {
				t.Fatalf("resolve value parity mismatch for %q: new=%q legacy=%q", surface, newTarget, legacyTarget)
			}
		})
	}
}

func TestNewDecideApprovalRenderTargetRegistry_ResolverHitMissAndUnsupportedFallbackParityWithLegacyInlineSetup(t *testing.T) {
	targets := map[DecideApprovalRenderSurface]DecideApprovalRenderTarget{
		DecideApprovalRenderSurfaceHTTP: DecideApprovalRenderTargetHTTP,
		DecideApprovalRenderSurfaceMCP:  DecideApprovalRenderTargetMCP,
	}

	newRegistry := newDecideApprovalRenderTargetRegistry(targets)
	legacyRegistry := projectiondispatch.NewPolicyRegistry(targets)

	tests := []struct {
		name            string
		surface         DecideApprovalRenderSurface
		wantDecideCalls int
		wantStatus      string
		wantRequestID   string
		wantHTTPError   *HTTPErrorContract
	}{
		{
			name:            "http resolver hit",
			surface:         DecideApprovalRenderSurfaceHTTP,
			wantDecideCalls: 1,
			wantStatus:      string(approval.DecisionDenied),
			wantRequestID:   "req-render-target-registry",
		},
		{
			name:            "mcp resolver hit",
			surface:         DecideApprovalRenderSurfaceMCP,
			wantDecideCalls: 1,
			wantStatus:      string(approval.DecisionDenied),
			wantRequestID:   "req-render-target-registry",
		},
		{
			name:            "resolver miss unsupported fallback",
			surface:         DecideApprovalRenderSurface("bogus"),
			wantDecideCalls: 0,
			wantHTTPError: &HTTPErrorContract{
				Status:  500,
				Code:    "internal_error",
				Message: `unsupported approval decide render target "bogus"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := orchestrateDecideApprovalForSurfaceWithRegistryCapture(newRegistry, tt.surface)
			legacyCapture := orchestrateDecideApprovalForSurfaceWithRegistryCapture(legacyRegistry, tt.surface)

			if !reflect.DeepEqual(newCapture, legacyCapture) {
				t.Fatalf("constructor parity mismatch for %q: new=%+v legacy=%+v", tt.surface, newCapture, legacyCapture)
			}

			if newCapture.decideCalls != tt.wantDecideCalls {
				t.Fatalf("unexpected decide invocation count for %q: got %d want %d", tt.surface, newCapture.decideCalls, tt.wantDecideCalls)
			}
			if newCapture.successStatus != tt.wantStatus {
				t.Fatalf("unexpected success status for %q: got %q want %q", tt.surface, newCapture.successStatus, tt.wantStatus)
			}
			if newCapture.successRequestID != tt.wantRequestID {
				t.Fatalf("unexpected request id for %q: got %q want %q", tt.surface, newCapture.successRequestID, tt.wantRequestID)
			}
			if !reflect.DeepEqual(newCapture.httpErr, tt.wantHTTPError) {
				t.Fatalf("unexpected HTTP fallback for %q: got %+v want %+v", tt.surface, newCapture.httpErr, tt.wantHTTPError)
			}
		})
	}
}

type decideApprovalRenderTargetRegistryFlowCapture struct {
	decideCalls      int
	successStatus    string
	successRequestID string
	httpErr          *HTTPErrorContract
}

func orchestrateDecideApprovalForSurfaceWithRegistryCapture(registry projectiondispatch.PolicyRegistry[DecideApprovalRenderSurface, DecideApprovalRenderTarget], surface DecideApprovalRenderSurface) decideApprovalRenderTargetRegistryFlowCapture {
	capture := decideApprovalRenderTargetRegistryFlowCapture{}
	request := &approval.Request{ID: "req-render-target-registry", Decision: approval.DecisionDenied}
	decide := func(*DecideApprovalRequest) (*ApprovalDecisionResult, error) {
		capture.decideCalls++
		return &ApprovalDecisionResult{Request: request}, nil
	}

	projection := orchestrateDecideApprovalForSurfaceWithRegistry(
		registry,
		strings.NewReader(`{"decision":"denied","decided_by":"operator"}`),
		decide,
		surface,
	)

	if projection != nil && projection.Success != nil {
		capture.successStatus = projection.Success.Status
		if projection.Success.Request != nil {
			capture.successRequestID = projection.Success.Request.ID
		}
	}
	if err, ok := projection.HTTPError(); ok {
		capture.httpErr = &HTTPErrorContract{Status: err.Status, Code: err.Code, Message: err.Message}
	}

	return capture
}

func orchestrateDecideApprovalForSurfaceWithRegistry(
	registry projectiondispatch.PolicyRegistry[DecideApprovalRenderSurface, DecideApprovalRenderTarget],
	body io.Reader,
	decide func(*DecideApprovalRequest) (*ApprovalDecisionResult, error),
	surface DecideApprovalRenderSurface,
) *DecideApprovalProjection {
	target, ok := registry.Resolve(surface)
	if !ok {
		return SelectDecideApprovalProjection(&DecideApprovalTransportContract{}, DecideApprovalRenderTarget(surface))
	}
	return OrchestrateDecideApproval(body, decide, target)
}
