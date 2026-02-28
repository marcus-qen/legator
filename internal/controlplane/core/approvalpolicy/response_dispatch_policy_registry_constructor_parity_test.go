package approvalpolicy

import (
	"fmt"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
)

func TestNewDecideApprovalResponseDispatchPolicyRegistry_ParityWithLegacyInlineSetup(t *testing.T) {
	policies := map[DecideApprovalRenderSurface]decideApprovalResponseDispatchPolicy{
		DecideApprovalRenderSurfaceHTTP: projectiondispatch.PolicyFunc[*DecideApprovalProjection, DecideApprovalResponseDispatchWriter](func(_ *DecideApprovalProjection, writer DecideApprovalResponseDispatchWriter) {
			if writer.WriteSuccess != nil {
				writer.WriteSuccess(&DecideApprovalSuccess{Status: "http"})
			}
		}),
		DecideApprovalRenderSurfaceMCP: projectiondispatch.PolicyFunc[*DecideApprovalProjection, DecideApprovalResponseDispatchWriter](func(_ *DecideApprovalProjection, writer DecideApprovalResponseDispatchWriter) {
			if writer.WriteSuccess != nil {
				writer.WriteSuccess(&DecideApprovalSuccess{Status: "mcp"})
			}
		}),
	}

	newRegistry := newDecideApprovalResponseDispatchPolicyRegistry(policies)
	legacyRegistry := projectiondispatch.NewPolicyRegistry(policies)

	policies[DecideApprovalRenderSurfaceHTTP] = projectiondispatch.PolicyFunc[*DecideApprovalProjection, DecideApprovalResponseDispatchWriter](func(_ *DecideApprovalProjection, writer DecideApprovalResponseDispatchWriter) {
		if writer.WriteSuccess != nil {
			writer.WriteSuccess(&DecideApprovalSuccess{Status: "mutated"})
		}
	})

	tests := []DecideApprovalRenderSurface{
		DecideApprovalRenderSurfaceHTTP,
		DecideApprovalRenderSurfaceMCP,
		DecideApprovalRenderSurface("bogus"),
	}

	for _, surface := range tests {
		t.Run(string(surface), func(t *testing.T) {
			newCapture := dispatchDecideApprovalPolicyRegistryForSurface(newRegistry, surface)
			legacyCapture := dispatchDecideApprovalPolicyRegistryForSurface(legacyRegistry, surface)
			if newCapture != legacyCapture {
				t.Fatalf("constructor parity mismatch for %q: new=%+v legacy=%+v", surface, newCapture, legacyCapture)
			}
		})
	}
}

type decideApprovalPolicyRegistryCapture struct {
	successCalled bool
	successStatus string
	errorCalled   bool
	errorText     string
}

func dispatchDecideApprovalPolicyRegistryForSurface(registry projectiondispatch.PolicyRegistry[DecideApprovalRenderSurface, decideApprovalResponseDispatchPolicy], surface DecideApprovalRenderSurface) decideApprovalPolicyRegistryCapture {
	capture := decideApprovalPolicyRegistryCapture{}

	projectiondispatch.DispatchForSurface(
		registry,
		surface,
		(*DecideApprovalProjection)(nil),
		DecideApprovalResponseDispatchWriter{
			WriteSuccess: func(success *DecideApprovalSuccess) {
				capture.successCalled = true
				if success != nil {
					capture.successStatus = success.Status
				}
			},
			WriteMCPError: func(err error) {
				capture.errorCalled = true
				if err != nil {
					capture.errorText = err.Error()
				}
			},
		},
		func(surface DecideApprovalRenderSurface, writer DecideApprovalResponseDispatchWriter) {
			if writer.WriteMCPError != nil {
				writer.WriteMCPError(fmt.Errorf("unsupported:%s", surface))
			}
		},
	)

	return capture
}
