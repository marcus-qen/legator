package projectiondispatch

import "testing"

func TestNewHTTPMCPDefaultPolicyRegistry_ParityWithLegacyInlineSetup(t *testing.T) {
	newRegistry := NewHTTPMCPDefaultPolicyRegistry(
		"http",
		dispatchDefaultHTTPMCPPolicyRegistryHTTP,
		"mcp",
		dispatchDefaultHTTPMCPPolicyRegistryMCP,
	)

	legacyRegistry := NewPolicyRegistry(map[string]Policy[int, *defaultHTTPMCPPolicyRegistryWriter]{
		"http": PolicyFunc[int, *defaultHTTPMCPPolicyRegistryWriter](dispatchDefaultHTTPMCPPolicyRegistryHTTP),
		"mcp":  PolicyFunc[int, *defaultHTTPMCPPolicyRegistryWriter](dispatchDefaultHTTPMCPPolicyRegistryMCP),
	})

	assertDefaultHTTPMCPPolicyRegistryDispatchParity(t, newRegistry, legacyRegistry)
}

func TestNewHTTPMCPDefaultPolicyRegistry_ParityWithIdentityPolicyRegistrySetup(t *testing.T) {
	newRegistry := NewHTTPMCPDefaultPolicyRegistry(
		"http",
		dispatchDefaultHTTPMCPPolicyRegistryHTTP,
		"mcp",
		dispatchDefaultHTTPMCPPolicyRegistryMCP,
	)

	legacyRegistry := NewHTTPMCPIdentityPolicyRegistry[string, Policy[int, *defaultHTTPMCPPolicyRegistryWriter]](
		"http",
		PolicyFunc[int, *defaultHTTPMCPPolicyRegistryWriter](dispatchDefaultHTTPMCPPolicyRegistryHTTP),
		"mcp",
		PolicyFunc[int, *defaultHTTPMCPPolicyRegistryWriter](dispatchDefaultHTTPMCPPolicyRegistryMCP),
	)

	assertDefaultHTTPMCPPolicyRegistryDispatchParity(t, newRegistry, legacyRegistry)
}

type defaultHTTPMCPPolicyRegistryWriter struct {
	text string
}

func dispatchDefaultHTTPMCPPolicyRegistryHTTP(_ int, writer *defaultHTTPMCPPolicyRegistryWriter) {
	writer.text = "http"
}

func dispatchDefaultHTTPMCPPolicyRegistryMCP(_ int, writer *defaultHTTPMCPPolicyRegistryWriter) {
	writer.text = "mcp"
}

func assertDefaultHTTPMCPPolicyRegistryDispatchParity(
	t *testing.T,
	newRegistry PolicyRegistry[string, Policy[int, *defaultHTTPMCPPolicyRegistryWriter]],
	legacyRegistry PolicyRegistry[string, Policy[int, *defaultHTTPMCPPolicyRegistryWriter]],
) {
	t.Helper()

	tests := []string{"http", "mcp", "bogus"}
	for _, surface := range tests {
		t.Run(surface, func(t *testing.T) {
			newCapture := dispatchDefaultHTTPMCPPolicyRegistryForSurface(newRegistry, surface)
			legacyCapture := dispatchDefaultHTTPMCPPolicyRegistryForSurface(legacyRegistry, surface)
			if newCapture != legacyCapture {
				t.Fatalf("default HTTP/MCP policy-registry parity mismatch for %q: new=%q legacy=%q", surface, newCapture, legacyCapture)
			}
		})
	}
}

func dispatchDefaultHTTPMCPPolicyRegistryForSurface(registry PolicyRegistry[string, Policy[int, *defaultHTTPMCPPolicyRegistryWriter]], surface string) string {
	writer := &defaultHTTPMCPPolicyRegistryWriter{}
	DispatchForSurface(
		registry,
		surface,
		0,
		writer,
		func(surface string, writer *defaultHTTPMCPPolicyRegistryWriter) {
			writer.text = "unsupported:" + surface
		},
	)
	return writer.text
}
