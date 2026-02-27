package projectiondispatch

// Policy is a projection dispatch policy.
//
// Implementations decide how a projection is emitted for a given surface.
type Policy[Projection any, Writer any] interface {
	Dispatch(projection Projection, writer Writer)
}

// PolicyFunc adapts a function into a dispatch policy.
type PolicyFunc[Projection any, Writer any] func(projection Projection, writer Writer)

// Dispatch satisfies the Policy interface.
func (f PolicyFunc[Projection, Writer]) Dispatch(projection Projection, writer Writer) {
	f(projection, writer)
}

// DispatchForSurface routes projection emission through the policy selected by
// the provided surface registry. Unsupported surfaces are delegated to
// onUnsupported so flows can preserve transport-specific fallback behavior.
func DispatchForSurface[Surface comparable, Projection any, Writer any](
	registry interface {
		Resolve(surface Surface) (Policy[Projection, Writer], bool)
	},
	surface Surface,
	projection Projection,
	writer Writer,
	onUnsupported func(surface Surface, writer Writer),
) {
	policy, ok := registry.Resolve(surface)
	if !ok {
		onUnsupported(surface, writer)
		return
	}
	policy.Dispatch(projection, writer)
}
