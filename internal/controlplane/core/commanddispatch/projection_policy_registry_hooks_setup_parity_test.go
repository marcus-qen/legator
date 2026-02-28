package commanddispatch

import (
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
	"github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"
)

func TestCommandProjectionResolverHooks_DefaultSetupParityWithLegacyInlineRegistrySetup(t *testing.T) {
	legacyDispatchRegistry := projectiondispatch.NewPolicyRegistry(map[ProjectionDispatchSurface]ProjectionDispatchSurface{
		ProjectionDispatchSurfaceHTTP: ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP:  ProjectionDispatchSurfaceMCP,
	})
	legacyReadRegistry := projectiondispatch.NewPolicyRegistry(map[ProjectionDispatchSurface]ProjectionDispatchSurface{
		ProjectionDispatchSurfaceHTTP: ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP:  ProjectionDispatchSurfaceMCP,
	})
	legacyInvokeRegistry := projectiondispatch.NewPolicyRegistry(map[ProjectionDispatchSurface]ProjectionDispatchSurface{
		ProjectionDispatchSurfaceHTTP: ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP:  ProjectionDispatchSurfaceMCP,
	})

	assertCommandProjectionResolverHooksParity(t, legacyDispatchRegistry, legacyReadRegistry, legacyInvokeRegistry)
}

func TestCommandProjectionResolverHooks_DefaultSetupParityWithDefaultIdentitySurfaceRegistryHelperFixture(t *testing.T) {
	legacyDispatchRegistry := projectiondispatch.NewHTTPMCPDefaultIdentitySurfaceRegistry(
		ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP,
	)
	legacyReadRegistry := projectiondispatch.NewHTTPMCPDefaultIdentitySurfaceRegistry(
		ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP,
	)
	legacyInvokeRegistry := projectiondispatch.NewHTTPMCPDefaultIdentitySurfaceRegistry(
		ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP,
	)

	assertCommandProjectionResolverHooksParity(t, legacyDispatchRegistry, legacyReadRegistry, legacyInvokeRegistry)
}

func assertCommandProjectionResolverHooksParity(
	t *testing.T,
	legacyDispatchRegistry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface],
	legacyReadRegistry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface],
	legacyInvokeRegistry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface],
) {
	t.Helper()

	tests := []ProjectionDispatchSurface{
		ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP,
		ProjectionDispatchSurface("bogus"),
	}

	for _, surface := range tests {
		t.Run(string(surface), func(t *testing.T) {
			legacyDispatchResolved, legacyDispatchOK := legacyDispatchRegistry.Resolve(surface)
			dispatchResolved, dispatchOK := ResolveCommandDispatchProjectionSurface(surface)
			if dispatchOK != legacyDispatchOK {
				t.Fatalf("dispatch resolver presence parity mismatch for %q: new=%v legacy=%v", surface, dispatchOK, legacyDispatchOK)
			}
			if dispatchResolved != legacyDispatchResolved {
				t.Fatalf("dispatch resolver value parity mismatch for %q: new=%q legacy=%q", surface, dispatchResolved, legacyDispatchResolved)
			}

			legacyReadResolved, legacyReadOK := legacyReadRegistry.Resolve(surface)
			readResolved, readOK := ResolveCommandReadProjectionSurface(surface)
			if readOK != legacyReadOK {
				t.Fatalf("read resolver presence parity mismatch for %q: new=%v legacy=%v", surface, readOK, legacyReadOK)
			}
			if readResolved != legacyReadResolved {
				t.Fatalf("read resolver value parity mismatch for %q: new=%q legacy=%q", surface, readResolved, legacyReadResolved)
			}

			legacyInvokeResolved, legacyInvokeOK := legacyInvokeRegistry.Resolve(surface)
			invokeResolved, invokeOK := ResolveCommandInvokeProjectionDispatchSurface(surface)
			if invokeOK != legacyInvokeOK {
				t.Fatalf("invoke resolver presence parity mismatch for %q: new=%v legacy=%v", surface, invokeOK, legacyInvokeOK)
			}
			if invokeResolved != legacyInvokeResolved {
				t.Fatalf("invoke resolver value parity mismatch for %q: new=%q legacy=%q", surface, invokeResolved, legacyInvokeResolved)
			}

			legacyTransport, legacyTransportOK := transportwriter.ResolveSurfaceToTransport(legacyInvokeRegistry, surface)
			resolvedTransport, transportOK := ResolveCommandInvokeTransportSurface(surface)
			if transportOK != legacyTransportOK {
				t.Fatalf("invoke transport resolver presence parity mismatch for %q: new=%v legacy=%v", surface, transportOK, legacyTransportOK)
			}
			if resolvedTransport != legacyTransport {
				t.Fatalf("invoke transport resolver value parity mismatch for %q: new=%q legacy=%q", surface, resolvedTransport, legacyTransport)
			}
		})
	}
}
