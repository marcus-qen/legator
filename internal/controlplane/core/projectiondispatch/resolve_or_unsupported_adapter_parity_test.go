package projectiondispatch

import "testing"

func TestDispatchResolvedOrUnsupported_ParityWithLegacyInlineBranch(t *testing.T) {
	tests := []struct {
		name    string
		surface string
		ok      bool
		want    string
	}{
		{name: "resolved surface", surface: "http", ok: true, want: "resolved-http"},
		{name: "unsupported surface", surface: "bogus", ok: false, want: "unsupported-bogus"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newLog := []string{}
			legacyLog := []string{}

			newResolveCalls, legacyResolveCalls := 0, 0
			newWriter, legacyWriter := 17, 17

			DispatchResolvedOrUnsupported(
				tt.surface,
				newWriter,
				func(surface string) (string, bool) {
					newResolveCalls++
					if !tt.ok {
						return "", false
					}
					return "resolved-" + surface, true
				},
				func(resolved string, writer int) {
					newLog = append(newLog, resolved)
					if writer != 17 {
						t.Fatalf("unexpected writer in resolved branch: %d", writer)
					}
				},
				func(surface string, writer int) {
					newLog = append(newLog, "unsupported-"+surface)
					if writer != 17 {
						t.Fatalf("unexpected writer in unsupported branch: %d", writer)
					}
				},
			)

			legacyDispatchResolvedOrUnsupportedInlineBranch(
				tt.surface,
				legacyWriter,
				func(surface string) (string, bool) {
					legacyResolveCalls++
					if !tt.ok {
						return "", false
					}
					return "resolved-" + surface, true
				},
				func(resolved string, writer int) {
					legacyLog = append(legacyLog, resolved)
					if writer != 17 {
						t.Fatalf("unexpected legacy writer in resolved branch: %d", writer)
					}
				},
				func(surface string, writer int) {
					legacyLog = append(legacyLog, "unsupported-"+surface)
					if writer != 17 {
						t.Fatalf("unexpected legacy writer in unsupported branch: %d", writer)
					}
				},
			)

			if newResolveCalls != legacyResolveCalls {
				t.Fatalf("resolve call parity mismatch: new=%d legacy=%d", newResolveCalls, legacyResolveCalls)
			}
			if len(newLog) != len(legacyLog) {
				t.Fatalf("branch log length mismatch: new=%v legacy=%v", newLog, legacyLog)
			}
			for i := range newLog {
				if newLog[i] != legacyLog[i] {
					t.Fatalf("branch log mismatch at %d: new=%q legacy=%q", i, newLog[i], legacyLog[i])
				}
			}
			if len(newLog) != 1 || newLog[0] != tt.want {
				t.Fatalf("unexpected branch log: got=%v want=[%q]", newLog, tt.want)
			}
		})
	}
}

func legacyDispatchResolvedOrUnsupportedInlineBranch[Surface any, ResolvedSurface any, Writer any](
	surface Surface,
	writer Writer,
	resolve func(surface Surface) (ResolvedSurface, bool),
	dispatchResolved func(resolvedSurface ResolvedSurface, writer Writer),
	dispatchUnsupported func(surface Surface, writer Writer),
) {
	resolvedSurface, ok := resolve(surface)
	if !ok {
		dispatchUnsupported(surface, writer)
		return
	}
	dispatchResolved(resolvedSurface, writer)
}
