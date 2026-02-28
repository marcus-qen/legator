package projectiondispatch

import (
	"reflect"
	"testing"
)

func TestNewHTTPMCPIdentityPolicySeedHelper_ParityWithLegacyInlineSeedSetup(t *testing.T) {
	newSeed := NewHTTPMCPIdentityPolicySeed("http", "http-policy", "mcp", "mcp-policy")
	legacySeed := map[string]string{
		"http": "http-policy",
		"mcp":  "mcp-policy",
	}

	if !reflect.DeepEqual(newSeed, legacySeed) {
		t.Fatalf("identity policy seed parity mismatch: new=%+v legacy=%+v", newSeed, legacySeed)
	}
}

func TestNewHTTPMCPIdentityPolicySeedHelper_ReturnsIndependentMapInstances(t *testing.T) {
	first := NewHTTPMCPIdentityPolicySeed("http", "http-policy", "mcp", "mcp-policy")
	second := NewHTTPMCPIdentityPolicySeed("http", "http-policy", "mcp", "mcp-policy")

	first["http"] = "mutated"
	if second["http"] != "http-policy" {
		t.Fatalf("expected independent seed map instances, second=%+v", second)
	}
}
