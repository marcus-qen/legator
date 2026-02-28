package projectiondispatch

import "testing"

func TestNewHTTPMCPDefaultResolverHookRegistryFixture_ParityWithDefaultIdentitySurfaceRegistryHelper(t *testing.T) {
	newRegistry := NewHTTPMCPDefaultResolverHookRegistryFixture[string]()
	legacyRegistry := NewHTTPMCPDefaultIdentitySurfaceRegistry("http", "mcp")

	assertDefaultHTTPMCPIdentitySurfaceRegistryParity(t, newRegistry, legacyRegistry)
}

func TestNewHTTPMCPDefaultResolverHookRegistryFixture_ParityWithLegacyInlineSetup(t *testing.T) {
	newRegistry := NewHTTPMCPDefaultResolverHookRegistryFixture[string]()
	legacyRegistry := NewPolicyRegistry(map[string]string{
		"http": "http",
		"mcp":  "mcp",
	})

	assertDefaultHTTPMCPIdentitySurfaceRegistryParity(t, newRegistry, legacyRegistry)
}
