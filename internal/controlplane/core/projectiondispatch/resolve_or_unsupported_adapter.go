package projectiondispatch

// DispatchResolvedOrUnsupported is a tiny shared branch seam for projection
// adapters that need to resolve a domain surface before dispatching.
//
// If resolve succeeds, dispatchResolved runs the policy path. Otherwise,
// dispatchUnsupported runs the domain fallback path.
func DispatchResolvedOrUnsupported[Surface any, ResolvedSurface any, Writer any](
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
