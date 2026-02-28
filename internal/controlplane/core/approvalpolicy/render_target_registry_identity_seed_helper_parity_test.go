package approvalpolicy

import (
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
)

func TestNewDefaultDecideApprovalRenderSurfaceRegistry_HTTPMCPIdentitySurfaceRegistryHelperParityWithLegacyInlineSetup(t *testing.T) {
	newRegistry := newDefaultDecideApprovalRenderSurfaceRegistry()
	legacyRegistry := projectiondispatch.NewPolicyRegistry(map[DecideApprovalRenderSurface]DecideApprovalRenderSurface{
		DecideApprovalRenderSurfaceHTTP: DecideApprovalRenderSurfaceHTTP,
		DecideApprovalRenderSurfaceMCP:  DecideApprovalRenderSurfaceMCP,
	})

	assertDefaultDecideApprovalRegistryParity(t, newRegistry, legacyRegistry)
}

func TestNewDefaultDecideApprovalRenderSurfaceRegistry_HTTPMCPIdentitySurfaceRegistryHelperParityWithLegacyComposedSetup(t *testing.T) {
	newRegistry := newDefaultDecideApprovalRenderSurfaceRegistry()
	legacyRegistry := newDecideApprovalRenderSurfaceRegistry(projectiondispatch.NewHTTPMCPIdentitySurfaceSeed(
		DecideApprovalRenderSurfaceHTTP,
		DecideApprovalRenderSurfaceMCP,
	))

	assertDefaultDecideApprovalRegistryParity(t, newRegistry, legacyRegistry)
}

func assertDefaultDecideApprovalRegistryParity(
	t *testing.T,
	newRegistry projectiondispatch.PolicyRegistry[DecideApprovalRenderSurface, DecideApprovalRenderSurface],
	legacyRegistry projectiondispatch.PolicyRegistry[DecideApprovalRenderSurface, DecideApprovalRenderSurface],
) {
	t.Helper()

	tests := []DecideApprovalRenderSurface{
		DecideApprovalRenderSurfaceHTTP,
		DecideApprovalRenderSurfaceMCP,
		DecideApprovalRenderSurface("bogus"),
	}

	for _, surface := range tests {
		t.Run(string(surface), func(t *testing.T) {
			newResolved, newOK := newRegistry.Resolve(surface)
			legacyResolved, legacyOK := legacyRegistry.Resolve(surface)
			if newOK != legacyOK {
				t.Fatalf("resolve presence parity mismatch for %q: new=%v legacy=%v", surface, newOK, legacyOK)
			}
			if newResolved != legacyResolved {
				t.Fatalf("resolve value parity mismatch for %q: new=%q legacy=%q", surface, newResolved, legacyResolved)
			}

			newCapture := orchestrateDecideApprovalForSurfaceWithRegistryCapture(newRegistry, surface)
			legacyCapture := orchestrateDecideApprovalForSurfaceWithRegistryCapture(legacyRegistry, surface)
			if !reflect.DeepEqual(newCapture, legacyCapture) {
				t.Fatalf("fallback parity mismatch for %q: new=%+v legacy=%+v", surface, newCapture, legacyCapture)
			}
		})
	}
}
