package transportwriter

// UnsupportedSurfaceEnvelopeBuilder renders unsupported-surface fallback
// envelopes for a specific transport scope.
type UnsupportedSurfaceEnvelopeBuilder func(surface string) *ResponseEnvelope

// UnsupportedSurfaceEnvelopeBuilderForScope returns a tiny shared seam that
// binds a scope to the legacy unsupported-surface message->envelope wiring.
func UnsupportedSurfaceEnvelopeBuilderForScope(scope UnsupportedSurfaceScope) UnsupportedSurfaceEnvelopeBuilder {
	return func(surface string) *ResponseEnvelope {
		return UnsupportedSurfaceEnvelope(UnsupportedSurfaceMessage(scope, surface))
	}
}
