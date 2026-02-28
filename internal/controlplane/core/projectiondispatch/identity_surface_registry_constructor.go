package projectiondispatch

// NewIdentitySurfaceRegistry constructs a surface resolver registry where
// supported surfaces resolve to themselves.
func NewIdentitySurfaceRegistry[Surface comparable](surfaces map[Surface]Surface) PolicyRegistry[Surface, Surface] {
	return NewPolicyRegistry(surfaces)
}
