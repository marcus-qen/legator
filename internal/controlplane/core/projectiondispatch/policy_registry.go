package projectiondispatch

// PolicyRegistry resolves surface-specific policies for projection adapters.
//
// The registry is generic so core flows can share the same selection seam while
// keeping flow-specific policy implementations local to each package.
type PolicyRegistry[Surface comparable, Policy any] struct {
	policies map[Surface]Policy
}

// NewPolicyRegistry constructs a registry from the provided policy map.
//
// The map is copied so callers can safely reuse or mutate their input without
// affecting registry behavior.
func NewPolicyRegistry[Surface comparable, Policy any](policies map[Surface]Policy) PolicyRegistry[Surface, Policy] {
	copied := make(map[Surface]Policy, len(policies))
	for surface, policy := range policies {
		copied[surface] = policy
	}
	return PolicyRegistry[Surface, Policy]{policies: copied}
}

// Resolve returns the policy mapped to a surface.
func (r PolicyRegistry[Surface, Policy]) Resolve(surface Surface) (Policy, bool) {
	policy, ok := r.policies[surface]
	return policy, ok
}
