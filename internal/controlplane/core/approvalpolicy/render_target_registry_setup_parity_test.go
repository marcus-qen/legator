package approvalpolicy

import (
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestDecideApprovalResolverHooks_DefaultSetupParityWithLegacyInlineRegistrySetup(t *testing.T) {
	legacySurfaceRegistry := projectiondispatch.NewPolicyRegistry(map[DecideApprovalRenderSurface]DecideApprovalRenderSurface{
		DecideApprovalRenderSurfaceHTTP: DecideApprovalRenderSurfaceHTTP,
		DecideApprovalRenderSurfaceMCP:  DecideApprovalRenderSurfaceMCP,
	})

	assertDecideApprovalResolverHooksParity(t, legacySurfaceRegistry)
}

func TestDecideApprovalResolverHooks_DefaultSetupParityWithDefaultIdentitySurfaceRegistryHelperFixture(t *testing.T) {
	legacySurfaceRegistry := projectiondispatch.NewHTTPMCPDefaultIdentitySurfaceRegistry(
		DecideApprovalRenderSurfaceHTTP,
		DecideApprovalRenderSurfaceMCP,
	)

	assertDecideApprovalResolverHooksParity(t, legacySurfaceRegistry)
}

func assertDecideApprovalResolverHooksParity(
	t *testing.T,
	legacySurfaceRegistry projectiondispatch.PolicyRegistry[DecideApprovalRenderSurface, DecideApprovalRenderSurface],
) {
	t.Helper()

	tests := []DecideApprovalRenderSurface{
		DecideApprovalRenderSurfaceHTTP,
		DecideApprovalRenderSurfaceMCP,
		DecideApprovalRenderSurface("bogus"),
	}

	for _, surface := range tests {
		t.Run(string(surface), func(t *testing.T) {
			legacyResolvedSurface, legacyResolveOK := legacySurfaceRegistry.Resolve(surface)
			legacyResolvedTarget := DecideApprovalRenderTarget(legacyResolvedSurface)

			resolvedTarget, resolveOK := ResolveDecideApprovalRenderTarget(surface)
			if resolveOK != legacyResolveOK {
				t.Fatalf("render target resolve presence parity mismatch for %q: new=%v legacy=%v", surface, resolveOK, legacyResolveOK)
			}
			if resolvedTarget != legacyResolvedTarget {
				t.Fatalf("render target resolve value parity mismatch for %q: new=%q legacy=%q", surface, resolvedTarget, legacyResolvedTarget)
			}

			legacyTransport, legacyTransportOK := transportwriter.ResolveSurfaceToTransport(legacySurfaceRegistry, surface)
			resolvedTransport, transportOK := ResolveDecideApprovalTransportSurface(surface)
			if transportOK != legacyTransportOK {
				t.Fatalf("transport resolve presence parity mismatch for %q: new=%v legacy=%v", surface, transportOK, legacyTransportOK)
			}
			if resolvedTransport != legacyTransport {
				t.Fatalf("transport resolve value parity mismatch for %q: new=%q legacy=%q", surface, resolvedTransport, legacyTransport)
			}
		})
	}
}
