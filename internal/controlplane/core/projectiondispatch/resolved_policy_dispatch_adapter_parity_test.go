package projectiondispatch

import "testing"

func TestDispatchResolvedPolicyForSurface_ParityWithLegacyNestedResolveAndDispatch(t *testing.T) {
	type writer struct {
		events []string
	}

	registry := NewPolicyRegistry(map[string]Policy[int, *writer]{
		"http": PolicyFunc[int, *writer](func(projection int, w *writer) {
			w.events = append(w.events, "policy-http")
			w.events = append(w.events, "projection")
			if projection != 7 {
				t.Fatalf("unexpected projection: got=%d want=7", projection)
			}
		}),
	})

	tests := []struct {
		name          string
		surface       string
		resolveOK     bool
		resolved      string
		wantNewEvents []string
	}{
		{
			name:          "supported resolved policy",
			surface:       "http",
			resolveOK:     true,
			resolved:      "http",
			wantNewEvents: []string{"policy-http", "projection"},
		},
		{
			name:          "resolved policy miss triggers unsupported passthrough",
			surface:       "alias",
			resolveOK:     true,
			resolved:      "mcp",
			wantNewEvents: []string{"unsupported-mcp"},
		},
		{
			name:          "resolve miss triggers unsupported",
			surface:       "bogus",
			resolveOK:     false,
			resolved:      "",
			wantNewEvents: []string{"unsupported-bogus"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newWriter := &writer{}
			legacyWriter := &writer{}

			newResolveCalls := 0
			legacyResolveCalls := 0

			newUnsupported := func(surface string, w *writer) {
				w.events = append(w.events, "unsupported-"+surface)
			}
			legacyUnsupported := func(surface string, w *writer) {
				w.events = append(w.events, "unsupported-"+surface)
			}

			DispatchResolvedPolicyForSurface(
				tt.surface,
				7,
				newWriter,
				func(surface string) (string, bool) {
					newResolveCalls++
					if !tt.resolveOK {
						return "", false
					}
					return tt.resolved, true
				},
				registry,
				newUnsupported,
			)

			legacyDispatchResolvedPolicyInlineBranch(
				tt.surface,
				7,
				legacyWriter,
				func(surface string) (string, bool) {
					legacyResolveCalls++
					if !tt.resolveOK {
						return "", false
					}
					return tt.resolved, true
				},
				registry,
				legacyUnsupported,
			)

			if newResolveCalls != legacyResolveCalls {
				t.Fatalf("resolve call parity mismatch: new=%d legacy=%d", newResolveCalls, legacyResolveCalls)
			}
			if len(newWriter.events) != len(legacyWriter.events) {
				t.Fatalf("event length parity mismatch: new=%v legacy=%v", newWriter.events, legacyWriter.events)
			}
			for i := range newWriter.events {
				if newWriter.events[i] != legacyWriter.events[i] {
					t.Fatalf("event parity mismatch at %d: new=%q legacy=%q", i, newWriter.events[i], legacyWriter.events[i])
				}
			}

			if len(newWriter.events) != len(tt.wantNewEvents) {
				t.Fatalf("unexpected event count: got=%v want=%v", newWriter.events, tt.wantNewEvents)
			}
			for i := range tt.wantNewEvents {
				if newWriter.events[i] != tt.wantNewEvents[i] {
					t.Fatalf("unexpected event at %d: got=%q want=%q", i, newWriter.events[i], tt.wantNewEvents[i])
				}
			}
		})
	}
}

func legacyDispatchResolvedPolicyInlineBranch[Surface comparable, Projection any, Writer any](
	surface Surface,
	projection Projection,
	writer Writer,
	resolve func(surface Surface) (Surface, bool),
	registry interface {
		Resolve(surface Surface) (Policy[Projection, Writer], bool)
	},
	onUnsupported func(surface Surface, writer Writer),
) {
	resolvedSurface, ok := resolve(surface)
	if !ok {
		onUnsupported(surface, writer)
		return
	}

	DispatchForSurface(registry, resolvedSurface, projection, writer, onUnsupported)
}
