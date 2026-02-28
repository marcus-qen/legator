package projectiondispatch

import "testing"

func TestDispatchUnsupportedSurfaceAdapterFallback_ParityWithLegacyInlineWiring(t *testing.T) {
	tests := []struct {
		name           string
		withHandledPtr bool
		handledBefore  bool
	}{
		{name: "without handled pointer", withHandledPtr: false, handledBefore: false},
		{name: "handled pointer false before", withHandledPtr: true, handledBefore: false},
		{name: "handled pointer true before", withHandledPtr: true, handledBefore: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newCalls, legacyCalls := 0, 0
			newHandled, legacyHandled := tt.handledBefore, tt.handledBefore

			var newHandledPtr *bool
			var legacyHandledPtr *bool
			if tt.withHandledPtr {
				newHandledPtr = &newHandled
				legacyHandledPtr = &legacyHandled
			}

			DispatchUnsupportedSurfaceAdapterFallback(
				"bogus",
				17,
				func(surface string, writer int) {
					if surface != "bogus" || writer != 17 {
						t.Fatalf("unexpected new fallback inputs: surface=%q writer=%d", surface, writer)
					}
					newCalls++
				},
				newHandledPtr,
			)

			legacyDispatchUnsupportedSurfaceAdapterFallback(
				"bogus",
				17,
				func(surface string, writer int) {
					if surface != "bogus" || writer != 17 {
						t.Fatalf("unexpected legacy fallback inputs: surface=%q writer=%d", surface, writer)
					}
					legacyCalls++
				},
				legacyHandledPtr,
			)

			if newCalls != legacyCalls {
				t.Fatalf("fallback call parity mismatch: new=%d legacy=%d", newCalls, legacyCalls)
			}
			if newHandled != legacyHandled {
				t.Fatalf("handled parity mismatch: new=%v legacy=%v", newHandled, legacyHandled)
			}
		})
	}
}

func legacyDispatchUnsupportedSurfaceAdapterFallback[Surface any, Writer any](
	surface Surface,
	writer Writer,
	dispatchFallback func(surface Surface, writer Writer),
	handled *bool,
) {
	dispatchFallback(surface, writer)
	if handled != nil {
		*handled = true
	}
}
