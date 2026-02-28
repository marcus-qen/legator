package projectiondispatch

import "testing"

func TestNewHTTPMCPDefaultIdentitySurfaceRegistry_ParityWithLegacyInlineSetup(t *testing.T) {
	newRegistry := NewHTTPMCPDefaultIdentitySurfaceRegistry("http", "mcp")
	legacyRegistry := NewPolicyRegistry(map[string]string{
		"http": "http",
		"mcp":  "mcp",
	})

	assertDefaultHTTPMCPIdentitySurfaceRegistryParity(t, newRegistry, legacyRegistry)
}

func TestNewHTTPMCPDefaultIdentitySurfaceRegistry_ParityWithIdentitySurfaceRegistrySetup(t *testing.T) {
	newRegistry := NewHTTPMCPDefaultIdentitySurfaceRegistry("http", "mcp")
	legacyRegistry := NewHTTPMCPIdentitySurfaceRegistry("http", "mcp")

	assertDefaultHTTPMCPIdentitySurfaceRegistryParity(t, newRegistry, legacyRegistry)
}

type defaultHTTPMCPIdentitySurfaceRegistryCapture struct {
	httpCalls int
	httpMsg   string
	mcpCalls  int
	mcpMsg    string
}

type defaultHTTPMCPIdentitySurfaceRegistryWriter struct {
	writeHTTP func(msg string)
	writeMCP  func(msg string)
}

func assertDefaultHTTPMCPIdentitySurfaceRegistryParity(
	t *testing.T,
	newRegistry PolicyRegistry[string, string],
	legacyRegistry PolicyRegistry[string, string],
) {
	t.Helper()

	tests := []struct {
		name        string
		surface     string
		withHTTP    bool
		withMCP     bool
		wantHTTPMsg string
		wantMCPMsg  string
	}{
		{name: "resolver hit http", surface: "http", withHTTP: true, withMCP: true, wantHTTPMsg: "resolved:http"},
		{name: "resolver hit mcp", surface: "mcp", withHTTP: true, withMCP: true, wantMCPMsg: "resolved:mcp"},
		{name: "resolver miss unsupported fallback prefers http", surface: "bogus", withHTTP: true, withMCP: true, wantHTTPMsg: "unsupported:bogus"},
		{name: "resolver miss unsupported fallback mcp when http absent", surface: "bogus", withHTTP: false, withMCP: true, wantMCPMsg: "unsupported:bogus"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCapture := dispatchDefaultHTTPMCPIdentitySurfaceWithRegistryCapture(newRegistry, tt.surface, tt.withHTTP, tt.withMCP)
			legacyCapture := dispatchDefaultHTTPMCPIdentitySurfaceWithRegistryCapture(legacyRegistry, tt.surface, tt.withHTTP, tt.withMCP)
			if newCapture != legacyCapture {
				t.Fatalf("default identity-surface registry parity mismatch for %q: new=%+v legacy=%+v", tt.surface, newCapture, legacyCapture)
			}

			assertDefaultHTTPMCPIdentitySurfaceRegistryCaptureSemantics(t, newCapture, tt.wantHTTPMsg, tt.wantMCPMsg)
		})
	}
}

func dispatchDefaultHTTPMCPIdentitySurfaceWithRegistryCapture(
	surfaceRegistry PolicyRegistry[string, string],
	surface string,
	withHTTP bool,
	withMCP bool,
) defaultHTTPMCPIdentitySurfaceRegistryCapture {
	capture := defaultHTTPMCPIdentitySurfaceRegistryCapture{}
	writer := defaultHTTPMCPIdentitySurfaceRegistryWriter{}
	if withHTTP {
		writer.writeHTTP = func(msg string) {
			capture.httpCalls++
			capture.httpMsg = msg
		}
	}
	if withMCP {
		writer.writeMCP = func(msg string) {
			capture.mcpCalls++
			capture.mcpMsg = msg
		}
	}

	policyRegistry := NewPolicyRegistry(map[string]Policy[*struct{}, defaultHTTPMCPIdentitySurfaceRegistryWriter]{
		"http": PolicyFunc[*struct{}, defaultHTTPMCPIdentitySurfaceRegistryWriter](func(_ *struct{}, writer defaultHTTPMCPIdentitySurfaceRegistryWriter) {
			if writer.writeHTTP != nil {
				writer.writeHTTP("resolved:http")
			}
		}),
		"mcp": PolicyFunc[*struct{}, defaultHTTPMCPIdentitySurfaceRegistryWriter](func(_ *struct{}, writer defaultHTTPMCPIdentitySurfaceRegistryWriter) {
			if writer.writeMCP != nil {
				writer.writeMCP("resolved:mcp")
			}
		}),
	})

	DispatchResolvedPolicyForSurface(
		surface,
		(*struct{})(nil),
		writer,
		func(candidate string) (string, bool) {
			return surfaceRegistry.Resolve(candidate)
		},
		policyRegistry,
		func(surface string, writer defaultHTTPMCPIdentitySurfaceRegistryWriter) {
			if writer.writeHTTP != nil {
				writer.writeHTTP("unsupported:" + surface)
				return
			}
			if writer.writeMCP != nil {
				writer.writeMCP("unsupported:" + surface)
			}
		},
	)

	return capture
}

func assertDefaultHTTPMCPIdentitySurfaceRegistryCaptureSemantics(
	t *testing.T,
	capture defaultHTTPMCPIdentitySurfaceRegistryCapture,
	wantHTTPMsg string,
	wantMCPMsg string,
) {
	t.Helper()

	if wantHTTPMsg == "" {
		if capture.httpCalls != 0 || capture.httpMsg != "" {
			t.Fatalf("unexpected HTTP capture output: %+v", capture)
		}
	} else {
		if capture.httpCalls != 1 || capture.httpMsg != wantHTTPMsg {
			t.Fatalf("unexpected HTTP capture output: %+v wantHTTPMsg=%q", capture, wantHTTPMsg)
		}
	}

	if wantMCPMsg == "" {
		if capture.mcpCalls != 0 || capture.mcpMsg != "" {
			t.Fatalf("unexpected MCP capture output: %+v", capture)
		}
		return
	}

	if capture.mcpCalls != 1 || capture.mcpMsg != wantMCPMsg {
		t.Fatalf("unexpected MCP capture output: %+v wantMCPMsg=%q", capture, wantMCPMsg)
	}
}
