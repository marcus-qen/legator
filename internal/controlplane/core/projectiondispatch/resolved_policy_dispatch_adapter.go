package projectiondispatch

// DispatchResolvedPolicyForSurface composes resolve-or-unsupported branching
// with policy-registry dispatch for projection adapters.
//
// The onUnsupported callback is passed through to both unsupported branches:
// resolver miss and resolved-surface policy miss.
func DispatchResolvedPolicyForSurface[Surface comparable, Projection any, Writer any](
	surface Surface,
	projection Projection,
	writer Writer,
	resolve func(surface Surface) (Surface, bool),
	registry interface {
		Resolve(surface Surface) (Policy[Projection, Writer], bool)
	},
	onUnsupported func(surface Surface, writer Writer),
) {
	DispatchResolvedOrUnsupported(
		surface,
		writer,
		resolve,
		func(resolvedSurface Surface, writer Writer) {
			DispatchForSurface(registry, resolvedSurface, projection, writer, onUnsupported)
		},
		onUnsupported,
	)
}
