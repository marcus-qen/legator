package approvalpolicy

import (
	"errors"
	"net/http"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/approval"
	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
)

func TestDefaultDecideApprovalResponseDispatchPolicyRegistry_ParityWithLegacyInlineSetup(t *testing.T) {
	legacyRegistry := newDecideApprovalResponseDispatchPolicyRegistry(map[DecideApprovalRenderSurface]decideApprovalResponseDispatchPolicy{
		DecideApprovalRenderSurfaceHTTP: projectiondispatch.PolicyFunc[*DecideApprovalProjection, DecideApprovalResponseDispatchWriter](dispatchDecideApprovalResponseHTTP),
		DecideApprovalRenderSurfaceMCP:  projectiondispatch.PolicyFunc[*DecideApprovalProjection, DecideApprovalResponseDispatchWriter](dispatchDecideApprovalResponseMCP),
	})

	tests := []struct {
		name       string
		surface    DecideApprovalRenderSurface
		projection *DecideApprovalProjection
	}{
		{
			name:    "http hit",
			surface: DecideApprovalRenderSurfaceHTTP,
			projection: ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(
				nil,
				&ApprovedDispatchError{Err: errors.New("probe p-default not connected")},
			)),
		},
		{
			name:    "mcp hit",
			surface: DecideApprovalRenderSurfaceMCP,
			projection: ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(
				&ApprovalDecisionResult{Request: &approval.Request{ID: "req-default", Decision: approval.DecisionApproved}},
				nil,
			)),
		},
		{
			name:       "resolver miss unsupported fallback",
			surface:    DecideApprovalRenderSurface("bogus"),
			projection: ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(&ApprovalDecisionResult{}, nil)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := dispatchDecideApprovalResponseWithRegistryCapture(defaultDecideApprovalResponseDispatchPolicyRegistry, tt.projection, tt.surface)
			legacyCapture := dispatchDecideApprovalResponseWithRegistryCapture(legacyRegistry, tt.projection, tt.surface)
			if newCapture != legacyCapture {
				t.Fatalf("default decide-approval registry parity mismatch for %q: new=%+v legacy=%+v", tt.surface, newCapture, legacyCapture)
			}
		})
	}
}

type decideApprovalDefaultPolicyRegistryCapture struct {
	successCalls int
	successState string
	successID    string
	httpErrCalls int
	httpStatus   int
	httpCode     string
	httpMessage  string
	mcpErrCalls  int
	mcpErrText   string
}

func dispatchDecideApprovalResponseWithRegistryCapture(
	registry projectiondispatch.PolicyRegistry[DecideApprovalRenderSurface, decideApprovalResponseDispatchPolicy],
	projection *DecideApprovalProjection,
	surface DecideApprovalRenderSurface,
) decideApprovalDefaultPolicyRegistryCapture {
	capture := decideApprovalDefaultPolicyRegistryCapture{}
	writer := DecideApprovalResponseDispatchWriter{
		WriteSuccess: func(success *DecideApprovalSuccess) {
			capture.successCalls++
			if success != nil {
				capture.successState = success.Status
				if success.Request != nil {
					capture.successID = success.Request.ID
				}
			}
		},
		WriteHTTPError: func(err *HTTPErrorContract) {
			capture.httpErrCalls++
			if err != nil {
				capture.httpStatus = err.Status
				capture.httpCode = err.Code
				capture.httpMessage = err.Message
			}
		},
		WriteMCPError: func(err error) {
			capture.mcpErrCalls++
			if err != nil {
				capture.mcpErrText = err.Error()
			}
		},
	}

	dispatchDecideApprovalResponseWithRegistry(projection, surface, writer, registry)
	return capture
}

func dispatchDecideApprovalResponseWithRegistry(
	projection *DecideApprovalProjection,
	surface DecideApprovalRenderSurface,
	writer DecideApprovalResponseDispatchWriter,
	registry projectiondispatch.PolicyRegistry[DecideApprovalRenderSurface, decideApprovalResponseDispatchPolicy],
) {
	projectiondispatch.DispatchResolvedPolicyForSurface(
		surface,
		projection,
		writer,
		resolveDecideApprovalResponseDispatchSurface,
		registry,
		dispatchUnsupportedDecideApprovalSurfaceAdapterFallback,
	)
}

func TestDefaultDecideApprovalResponseDispatchPolicyRegistry_UnsupportedSurfaceHTTPFirstFallbackLock(t *testing.T) {
	capture := dispatchDecideApprovalResponseWithRegistryCapture(
		defaultDecideApprovalResponseDispatchPolicyRegistry,
		ProjectDecideApprovalTransport(EncodeDecideApprovalTransport(&ApprovalDecisionResult{}, nil)),
		DecideApprovalRenderSurface("bogus"),
	)
	if capture.httpErrCalls != 1 || capture.mcpErrCalls != 0 {
		t.Fatalf("fallback precedence mismatch: http=%d mcp=%d", capture.httpErrCalls, capture.mcpErrCalls)
	}
	if capture.httpStatus != http.StatusInternalServerError || capture.httpCode != "internal_error" {
		t.Fatalf("unexpected unsupported-surface HTTP contract: %+v", capture)
	}
}
