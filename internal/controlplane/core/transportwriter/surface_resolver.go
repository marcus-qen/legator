package transportwriter

// ResolveSurfaceToTransport resolves a domain-specific response surface through
// a registry and converts the resolved value into the shared transport surface.
//
// The generic seam lets multiple domains share one surface->transport resolver
// without duplicating mapping helpers in each package.
func ResolveSurfaceToTransport[InputSurface comparable, ResolvedSurface ~string](
	resolver interface {
		Resolve(surface InputSurface) (ResolvedSurface, bool)
	},
	surface InputSurface,
) (Surface, bool) {
	resolved, ok := resolver.Resolve(surface)
	if !ok {
		return "", false
	}
	return Surface(resolved), true
}
