package projectiondispatch

import (
	"reflect"
	"testing"
)

func TestNewHTTPMCPIdentitySeedHelper_ParityWithLegacyInlineSeedSetup(t *testing.T) {
	newSeed := NewHTTPMCPIdentitySurfaceSeed("http", "mcp")
	legacySeed := map[string]string{
		"http": "http",
		"mcp":  "mcp",
	}

	if !reflect.DeepEqual(newSeed, legacySeed) {
		t.Fatalf("identity seed parity mismatch: new=%+v legacy=%+v", newSeed, legacySeed)
	}
}

func TestNewHTTPMCPIdentitySeedHelper_ReturnsIndependentMapInstances(t *testing.T) {
	first := NewHTTPMCPIdentitySurfaceSeed("http", "mcp")
	second := NewHTTPMCPIdentitySurfaceSeed("http", "mcp")

	first["http"] = "mutated"
	if second["http"] != "http" {
		t.Fatalf("expected independent seed map instances, second=%+v", second)
	}
}
