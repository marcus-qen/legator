package projectiondispatch

import "testing"

func TestNewIdentitySurfaceRegistry_ParityWithNewPolicyRegistry(t *testing.T) {
	surfaces := map[string]string{
		"http": "http",
		"mcp":  "mcp",
	}

	newRegistry := NewIdentitySurfaceRegistry(surfaces)
	legacyRegistry := NewPolicyRegistry(surfaces)
	surfaces["http"] = "mutated"

	tests := []string{"http", "mcp", "bogus"}
	for _, surface := range tests {
		t.Run(surface, func(t *testing.T) {
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
}
