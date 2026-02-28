package projectiondispatch

import (
	"fmt"
	"testing"
)

func TestNewHTTPMCPIdentityPolicyRegistry_ParityWithLegacyInlineSetup(t *testing.T) {
	httpPolicy := Policy[int, *identityPolicyRegistryWriter](PolicyFunc[int, *identityPolicyRegistryWriter](func(_ int, writer *identityPolicyRegistryWriter) {
		writer.text = "http"
	}))
	mcpPolicy := Policy[int, *identityPolicyRegistryWriter](PolicyFunc[int, *identityPolicyRegistryWriter](func(_ int, writer *identityPolicyRegistryWriter) {
		writer.text = "mcp"
	}))

	newRegistry := NewHTTPMCPIdentityPolicyRegistry("http", httpPolicy, "mcp", mcpPolicy)
	legacyRegistry := NewPolicyRegistry(map[string]Policy[int, *identityPolicyRegistryWriter]{
		"http": httpPolicy,
		"mcp":  mcpPolicy,
	})

	assertIdentityPolicyRegistryDispatchParity(t, newRegistry, legacyRegistry)
}

func TestNewHTTPMCPIdentityPolicyRegistry_ParityWithComposedSeedAndPolicyRegistrySetup(t *testing.T) {
	httpPolicy := Policy[int, *identityPolicyRegistryWriter](PolicyFunc[int, *identityPolicyRegistryWriter](func(_ int, writer *identityPolicyRegistryWriter) {
		writer.text = "http"
	}))
	mcpPolicy := Policy[int, *identityPolicyRegistryWriter](PolicyFunc[int, *identityPolicyRegistryWriter](func(_ int, writer *identityPolicyRegistryWriter) {
		writer.text = "mcp"
	}))

	newRegistry := NewHTTPMCPIdentityPolicyRegistry("http", httpPolicy, "mcp", mcpPolicy)
	legacyRegistry := NewPolicyRegistry(NewHTTPMCPIdentityPolicySeed("http", httpPolicy, "mcp", mcpPolicy))

	assertIdentityPolicyRegistryDispatchParity(t, newRegistry, legacyRegistry)
}

type identityPolicyRegistryWriter struct {
	text string
}

func assertIdentityPolicyRegistryDispatchParity(
	t *testing.T,
	newRegistry PolicyRegistry[string, Policy[int, *identityPolicyRegistryWriter]],
	legacyRegistry PolicyRegistry[string, Policy[int, *identityPolicyRegistryWriter]],
) {
	t.Helper()

	tests := []string{"http", "mcp", "bogus"}
	for _, surface := range tests {
		t.Run(surface, func(t *testing.T) {
			newCapture := dispatchIdentityPolicyRegistryForSurface(newRegistry, surface)
			legacyCapture := dispatchIdentityPolicyRegistryForSurface(legacyRegistry, surface)
			if newCapture != legacyCapture {
				t.Fatalf("identity policy registry parity mismatch for %q: new=%q legacy=%q", surface, newCapture, legacyCapture)
			}
		})
	}
}

func dispatchIdentityPolicyRegistryForSurface(registry PolicyRegistry[string, Policy[int, *identityPolicyRegistryWriter]], surface string) string {
	writer := &identityPolicyRegistryWriter{}
	DispatchForSurface(
		registry,
		surface,
		0,
		writer,
		func(surface string, writer *identityPolicyRegistryWriter) {
			writer.text = fmt.Sprintf("unsupported:%s", surface)
		},
	)
	return writer.text
}
