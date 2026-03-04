// Package sandbox wasm_lane.go provides WASM fast lane constants, validators,
// and helpers for the sandbox subsystem.
package sandbox

import "fmt"

// WASM lane resource defaults. These are applied by the lane registry and
// should match the built-in wasm-fast-lane policy template.
const (
	// WASMMemoryCapMiB is the default memory ceiling for WASM lane tasks.
	WASMMemoryCapMiB = 256

	// WASMCPUCapMillis is the default CPU limit for WASM lane tasks in millicores.
	WASMCPUCapMillis = 500

	// WASMMaxRunSecs is the default maximum runtime for WASM tasks in seconds.
	WASMMaxRunSecs = 300

	// WASMFastLaneTemplateID is the policy template applied to WASM lane sandboxes.
	WASMFastLaneTemplateID = "wasm-fast-lane"
)

// ValidateWASMLaneSession checks that a session intended for the WASM fast lane
// is configured correctly: it must use the wasmtime runtime class and must not
// have host-direct allowed.
func ValidateWASMLaneSession(runtimeClass string, hostDirectAllowed bool) error {
	if runtimeClass != RuntimeClassWASM {
		return fmt.Errorf("WASM lane requires runtime_class %q, got %q", RuntimeClassWASM, runtimeClass)
	}
	if hostDirectAllowed {
		return fmt.Errorf("WASM lane does not permit host-direct execution")
	}
	return nil
}

// WASMDeniedOperations returns the list of operations explicitly denied in the
// WASM fast lane. Enforced by the policy engine and validated at task submission.
func WASMDeniedOperations() []string {
	return []string{
		"network_egress",    // no outbound TCP/UDP
		"host_fs_write",     // no writes to the host filesystem
		"mount",             // no volume mounts outside provided inputs
		"exec_host_process", // no executing host-side processes
		"raw_socket",        // no raw socket access
	}
}

// IsWASMDeniedOperation returns true when the given operation name is
// disallowed in the WASM fast lane.
func IsWASMDeniedOperation(op string) bool {
	for _, denied := range WASMDeniedOperations() {
		if op == denied {
			return true
		}
	}
	return false
}
