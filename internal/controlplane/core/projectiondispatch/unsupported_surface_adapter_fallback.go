package projectiondispatch

// DispatchUnsupportedSurfaceAdapterFallback centralizes unsupported-surface
// adapter fallback dispatch and optional handled-flag wiring.
func DispatchUnsupportedSurfaceAdapterFallback[Surface any, Writer any](
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
