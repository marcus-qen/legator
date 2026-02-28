package projectiondispatch

import "testing"

func TestNewHTTPMCPDefaultIdentitySurfaceRegistryFixture_ParityWithLegacySeedWiring(t *testing.T) {
	constructor := func(surfaces map[string]string) PolicyRegistry[string, string] {
		return NewIdentitySurfaceRegistry(surfaces)
	}

	newRegistry := NewHTTPMCPDefaultIdentitySurfaceRegistryFixture(
		constructor,
		"http",
		"mcp",
	)

	legacyRegistry := constructor(NewHTTPMCPIdentitySurfaceSeed("http", "mcp"))

	assertDefaultHTTPMCPIdentitySurfaceRegistryParity(t, newRegistry, legacyRegistry)
}
