package commanddispatch

import (
	"errors"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/core/projectiondispatch"
)

func TestNewCommandProjectionSurfaceRegistries_ResolveParityWithLegacyInlineSetup(t *testing.T) {
	constructors := []struct {
		name        string
		constructor func(map[ProjectionDispatchSurface]ProjectionDispatchSurface) projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface]
	}{
		{name: "dispatch", constructor: newCommandDispatchProjectionSurfaceRegistry},
		{name: "read", constructor: newCommandReadProjectionSurfaceRegistry},
		{name: "invoke", constructor: newCommandInvokeProjectionSurfaceRegistry},
	}

	tests := []ProjectionDispatchSurface{
		ProjectionDispatchSurfaceHTTP,
		ProjectionDispatchSurfaceMCP,
		ProjectionDispatchSurface("bogus"),
	}

	for _, registryCase := range constructors {
		t.Run(registryCase.name, func(t *testing.T) {
			surfaces := map[ProjectionDispatchSurface]ProjectionDispatchSurface{
				ProjectionDispatchSurfaceHTTP: ProjectionDispatchSurfaceHTTP,
				ProjectionDispatchSurfaceMCP:  ProjectionDispatchSurfaceMCP,
			}

			newRegistry := registryCase.constructor(surfaces)
			legacyRegistry := projectiondispatch.NewPolicyRegistry(surfaces)
			surfaces[ProjectionDispatchSurfaceHTTP] = ProjectionDispatchSurface("mutated")

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
				})
			}
		})
	}
}

func TestNewCommandProjectionSurfaceRegistries_ResolverHitMissAndUnsupportedFallbackParityWithLegacyInlineSetup(t *testing.T) {
	constructors := []struct {
		name        string
		constructor func(map[ProjectionDispatchSurface]ProjectionDispatchSurface) projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface]
	}{
		{name: "dispatch", constructor: newCommandDispatchProjectionSurfaceRegistry},
		{name: "read", constructor: newCommandReadProjectionSurfaceRegistry},
		{name: "invoke", constructor: newCommandInvokeProjectionSurfaceRegistry},
	}

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

	for _, registryCase := range constructors {
		t.Run(registryCase.name, func(t *testing.T) {
			surfaces := map[ProjectionDispatchSurface]ProjectionDispatchSurface{
				ProjectionDispatchSurfaceHTTP: ProjectionDispatchSurfaceHTTP,
				ProjectionDispatchSurfaceMCP:  ProjectionDispatchSurfaceMCP,
			}

			newRegistry := registryCase.constructor(surfaces)
			legacyRegistry := projectiondispatch.NewPolicyRegistry(surfaces)

			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					newCapture := dispatchCommandProjectionSurfaceViaRegistry(newRegistry, tt.surface, tt.withHTTP, tt.withMCP)
					legacyCapture := dispatchCommandProjectionSurfaceViaRegistry(legacyRegistry, tt.surface, tt.withHTTP, tt.withMCP)

					if !newCapture.equal(legacyCapture) {
						t.Fatalf("constructor parity mismatch for %q: new=%+v legacy=%+v", tt.surface, newCapture, legacyCapture)
					}

					assertCommandProjectionSurfaceCaptureSemantics(t, newCapture, tt.wantHTTPMsg, tt.wantMCPMsg)
				})
			}
		})
	}
}

func dispatchCommandProjectionSurfaceViaRegistry(
	surfaceRegistry projectiondispatch.PolicyRegistry[ProjectionDispatchSurface, ProjectionDispatchSurface],
	surface ProjectionDispatchSurface,
	withHTTP bool,
	withMCP bool,
) commandUnsupportedFallbackCapture {
	capture := commandUnsupportedFallbackCapture{}
	writer := capture.writer(withHTTP, withMCP)

	policyRegistry := projectiondispatch.NewPolicyRegistry(map[ProjectionDispatchSurface]projectiondispatch.Policy[*struct{}, CommandProjectionDispatchWriter]{
		ProjectionDispatchSurfaceHTTP: projectiondispatch.PolicyFunc[*struct{}, CommandProjectionDispatchWriter](func(_ *struct{}, writer CommandProjectionDispatchWriter) {
			if writer.WriteHTTPError != nil {
				writer.WriteHTTPError(&HTTPErrorContract{Status: 299, Code: "resolved", Message: "resolved:http"})
			}
		}),
		ProjectionDispatchSurfaceMCP: projectiondispatch.PolicyFunc[*struct{}, CommandProjectionDispatchWriter](func(_ *struct{}, writer CommandProjectionDispatchWriter) {
			if writer.WriteMCPError != nil {
				writer.WriteMCPError(errors.New("resolved:mcp"))
			}
		}),
	})

	projectiondispatch.DispatchResolvedPolicyForSurface(
		surface,
		(*struct{})(nil),
		writer,
		func(candidate ProjectionDispatchSurface) (ProjectionDispatchSurface, bool) {
			return surfaceRegistry.Resolve(candidate)
		},
		policyRegistry,
		func(surface ProjectionDispatchSurface, writer CommandProjectionDispatchWriter) {
			dispatchUnsupportedCommandDispatchSurfaceFallback(surface, writer)
		},
	)

	return capture
}

func assertCommandProjectionSurfaceCaptureSemantics(t *testing.T, capture commandUnsupportedFallbackCapture, wantHTTPMsg string, wantMCPMsg string) {
	t.Helper()

	if wantHTTPMsg == "" {
		if capture.httpErrCalled || capture.httpErr != nil {
			t.Fatalf("unexpected HTTP callback output: %+v", capture.httpErr)
		}
	} else {
		if !capture.httpErrCalled || capture.httpErr == nil {
			t.Fatalf("expected HTTP callback output, got %+v", capture.httpErr)
		}
		if capture.httpErr.Message != wantHTTPMsg {
			t.Fatalf("unexpected HTTP message: got %q want %q", capture.httpErr.Message, wantHTTPMsg)
		}
	}

	if wantMCPMsg == "" {
		if capture.mcpErrCalled || capture.mcpErr != nil {
			t.Fatalf("unexpected MCP callback output: %v", capture.mcpErr)
		}
		return
	}

	if !capture.mcpErrCalled || capture.mcpErr == nil {
		t.Fatalf("expected MCP callback output, got %v", capture.mcpErr)
	}
	if capture.mcpErr.Error() != wantMCPMsg {
		t.Fatalf("unexpected MCP message: got %q want %q", capture.mcpErr.Error(), wantMCPMsg)
	}
}
