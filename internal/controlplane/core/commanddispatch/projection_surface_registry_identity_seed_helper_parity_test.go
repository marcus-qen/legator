package commanddispatch

import (
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
)

func TestNewDefaultCommandProjectionSurfaceRegistries_HTTPMCPIdentitySurfaceRegistryHelperParityWithLegacyInlineSetup(t *testing.T) {
	constructors := []struct {
		name        string
		constructor func() projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface]
	}{
		{name: "dispatch", constructor: newDefaultCommandDispatchProjectionSurfaceRegistry},
		{name: "read", constructor: newDefaultCommandReadProjectionSurfaceRegistry},
		{name: "invoke", constructor: newDefaultCommandInvokeProjectionSurfaceRegistry},
	}

	for _, registryCase := range constructors {
		t.Run(registryCase.name, func(t *testing.T) {
			newRegistry := registryCase.constructor()
			legacyRegistry := projectiondispatch.NewPolicyRegistry(map[ProjectionDispatchSurface]ProjectionDispatchSurface{
				ProjectionDispatchSurfaceHTTP: ProjectionDispatchSurfaceHTTP,
				ProjectionDispatchSurfaceMCP:  ProjectionDispatchSurfaceMCP,
			})

			assertDefaultCommandProjectionSurfaceRegistryParity(t, newRegistry, legacyRegistry)
		})
	}
}

func TestNewDefaultCommandProjectionSurfaceRegistries_HTTPMCPIdentitySurfaceRegistryHelperParityWithLegacyComposedSetup(t *testing.T) {
	constructors := []struct {
		name        string
		constructor func() projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface]
	}{
		{name: "dispatch", constructor: newDefaultCommandDispatchProjectionSurfaceRegistry},
		{name: "read", constructor: newDefaultCommandReadProjectionSurfaceRegistry},
		{name: "invoke", constructor: newDefaultCommandInvokeProjectionSurfaceRegistry},
	}

	legacyConstructors := map[string]func() projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface]{
		"dispatch": func() projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface] {
			return newCommandDispatchProjectionSurfaceRegistry(projectiondispatch.NewHTTPMCPIdentitySurfaceSeed(
				ProjectionDispatchSurfaceHTTP,
				ProjectionDispatchSurfaceMCP,
			))
		},
		"read": func() projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface] {
			return newCommandReadProjectionSurfaceRegistry(projectiondispatch.NewHTTPMCPIdentitySurfaceSeed(
				ProjectionDispatchSurfaceHTTP,
				ProjectionDispatchSurfaceMCP,
			))
		},
		"invoke": func() projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface] {
			return newCommandInvokeProjectionSurfaceRegistry(projectiondispatch.NewHTTPMCPIdentitySurfaceSeed(
				ProjectionDispatchSurfaceHTTP,
				ProjectionDispatchSurfaceMCP,
			))
		},
	}

	for _, registryCase := range constructors {
		t.Run(registryCase.name, func(t *testing.T) {
			newRegistry := registryCase.constructor()
			legacyRegistry := legacyConstructors[registryCase.name]()

			assertDefaultCommandProjectionSurfaceRegistryParity(t, newRegistry, legacyRegistry)
		})
	}
}

func assertDefaultCommandProjectionSurfaceRegistryParity(
	t *testing.T,
	newRegistry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface],
	legacyRegistry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface],
) {
	t.Helper()

	tests := []struct {
		name        string
		surface     ProjectionDispatchSurface
		withHTTP    bool
		withMCP     bool
		wantHTTPMsg string
		wantMCPMsg  string
	}{
		{name: "resolver hit http", surface: ProjectionDispatchSurfaceHTTP, withHTTP: true, withMCP: true, wantHTTPMsg: "resolved:http"},
		{name: "resolver hit mcp", surface: ProjectionDispatchSurfaceMCP, withHTTP: true, withMCP: true, wantMCPMsg: "resolved:mcp"},
		{name: "resolver miss unsupported fallback prefers http", surface: ProjectionDispatchSurface("bogus"), withHTTP: true, withMCP: true, wantHTTPMsg: `unsupported command dispatch surface "bogus"`},
		{name: "resolver miss unsupported fallback mcp when http absent", surface: ProjectionDispatchSurface("bogus"), withHTTP: false, withMCP: true, wantMCPMsg: `unsupported command dispatch surface "bogus"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := dispatchCommandProjectionSurfaceViaRegistry(newRegistry, tt.surface, tt.withHTTP, tt.withMCP)
			legacyCapture := dispatchCommandProjectionSurfaceViaRegistry(legacyRegistry, tt.surface, tt.withHTTP, tt.withMCP)

			if !newCapture.equal(legacyCapture) {
				t.Fatalf("identity surface registry helper parity mismatch for %q: new=%+v legacy=%+v", tt.surface, newCapture, legacyCapture)
			}

			assertCommandProjectionSurfaceCaptureSemantics(t, newCapture, tt.wantHTTPMsg, tt.wantMCPMsg)
		})
	}
}
