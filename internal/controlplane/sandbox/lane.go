package sandbox

import "fmt"

// Lane names available to sandbox creation requests.
const (
	LaneContainer = "container"
	LaneKata      = "kata"
	LaneWasm      = "wasm"
)

// LaneDefaults holds the template defaults applied when a named lane is selected.
type LaneDefaults struct {
	// RuntimeClass is the Kubernetes runtime class for this lane.
	RuntimeClass string

	// TemplateID is the policy template to apply.
	TemplateID string

	// CPUMillis is the default CPU limit in millicores (e.g. 500 = 0.5 CPU).
	CPUMillis int

	// MemoryMiB is the default memory limit in MiB.
	MemoryMiB int

	// AllowedCapabilities lists Linux capabilities permitted for this lane.
	// An empty slice means no additional capabilities (minimum set).
	AllowedCapabilities []string

	// HostDirectAllowed indicates whether host-direct execution is ever
	// permitted in this lane. For sandbox lanes this is always false.
	HostDirectAllowed bool
}

// laneRegistry maps lane names to their defaults.
var laneRegistry = map[string]LaneDefaults{
	LaneContainer: {
		RuntimeClass:        "runc",
		TemplateID:          "diagnose",
		CPUMillis:           1000,
		MemoryMiB:           512,
		AllowedCapabilities: []string{},
		HostDirectAllowed:   false,
	},
	LaneKata: {
		RuntimeClass:        "kata-containers",
		TemplateID:          "full-remediate",
		CPUMillis:           2000,
		MemoryMiB:           1024,
		AllowedCapabilities: []string{},
		HostDirectAllowed:   false,
	},
	LaneWasm: {
		RuntimeClass:        "wasmtime",
		TemplateID:          "wasm-fast-lane",
		CPUMillis:           500,
		MemoryMiB:           256,
		AllowedCapabilities: []string{},
		HostDirectAllowed:   false,
	},
}

// ResolveLane returns the LaneDefaults for the named lane, or an error if the
// lane name is not registered.
func ResolveLane(lane string) (LaneDefaults, error) {
	if lane == "" {
		return LaneDefaults{}, nil
	}
	d, ok := laneRegistry[lane]
	if !ok {
		return LaneDefaults{}, fmt.Errorf("unknown lane %q: must be one of container, kata, wasm", lane)
	}
	return d, nil
}

// KnownLanes returns all registered lane names.
func KnownLanes() []string {
	names := make([]string, 0, len(laneRegistry))
	for name := range laneRegistry {
		names = append(names, name)
	}
	return names
}
