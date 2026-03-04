package policy

import (
	"testing"

	"github.com/marcus-qen/legator/internal/protocol"
)

// ── IsWasmLane ────────────────────────────────────────────────────────────────

func TestIsWasmLane(t *testing.T) {
	if !IsWasmLane(protocol.ExecWasmSandbox) {
		t.Error("expected ExecWasmSandbox to be recognised as WASM lane")
	}
	if IsWasmLane(protocol.ExecObserveDirect) {
		t.Error("expected ExecObserveDirect to NOT be WASM lane")
	}
	if IsWasmLane(protocol.ExecRemediateSandbox) {
		t.Error("expected ExecRemediateSandbox to NOT be WASM lane")
	}
}

// ── WasmLaneRejectsHostDirect ─────────────────────────────────────────────────

func TestWasmLaneRejectsHostDirect(t *testing.T) {
	tests := []struct {
		name          string
		sandboxLane   protocol.ExecutionClass
		requestedLane protocol.ExecutionClass
		category      string
		want          bool
	}{
		{
			name:          "wasm sandbox + host-direct mutation → reject",
			sandboxLane:   protocol.ExecWasmSandbox,
			requestedLane: protocol.ExecObserveDirect,
			category:      "mutation",
			want:          true,
		},
		{
			name:          "wasm sandbox + breakglass-direct mutation → reject",
			sandboxLane:   protocol.ExecWasmSandbox,
			requestedLane: protocol.ExecBreakglassDirect,
			category:      "mutation",
			want:          true,
		},
		{
			name:          "wasm sandbox + sandboxed mutation → allow",
			sandboxLane:   protocol.ExecWasmSandbox,
			requestedLane: protocol.ExecRemediateSandbox,
			category:      "mutation",
			want:          false,
		},
		{
			name:          "wasm sandbox + host-direct observe → allow (not a mutation)",
			sandboxLane:   protocol.ExecWasmSandbox,
			requestedLane: protocol.ExecObserveDirect,
			category:      "observe",
			want:          false,
		},
		{
			name:          "kata sandbox + host-direct mutation → not wasm lane, allow",
			sandboxLane:   protocol.ExecRemediateSandbox,
			requestedLane: protocol.ExecObserveDirect,
			category:      "mutation",
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WasmLaneRejectsHostDirect(tt.sandboxLane, tt.requestedLane, tt.category)
			if got != tt.want {
				t.Fatalf("WasmLaneRejectsHostDirect(%q,%q,%q) = %v, want %v",
					tt.sandboxLane, tt.requestedLane, tt.category, got, tt.want)
			}
		})
	}
}

// ── WASM template in policy store ────────────────────────────────────────────

func TestNewStore_HasWasmFastLane(t *testing.T) {
	s := NewStore()

	tpl, ok := s.Get("wasm-fast-lane")
	if !ok {
		t.Fatal("wasm-fast-lane template missing from store")
	}
	if tpl.ExecutionClassRequired != protocol.ExecWasmSandbox {
		t.Errorf("expected ExecWasmSandbox, got %q", tpl.ExecutionClassRequired)
	}
	if !tpl.SandboxRequired {
		t.Error("expected SandboxRequired=true for wasm-fast-lane")
	}
	if tpl.RuntimeClass != "wasmtime" {
		t.Errorf("expected runtime_class wasmtime, got %q", tpl.RuntimeClass)
	}
	if tpl.CPUMillis != 500 {
		t.Errorf("expected cpu_millis 500, got %d", tpl.CPUMillis)
	}
	if tpl.MemoryMiB != 256 {
		t.Errorf("expected memory_mib 256, got %d", tpl.MemoryMiB)
	}
	if tpl.MaxRuntimeSec != 300 {
		t.Errorf("expected max_runtime_sec 300, got %d", tpl.MaxRuntimeSec)
	}
	if tpl.ApprovalMode != protocol.ApprovalMutationGate {
		t.Errorf("expected approval_mode mutation_gate, got %q", tpl.ApprovalMode)
	}
}

func TestNewStore_WasmFastLane_ToPolicy(t *testing.T) {
	s := NewStore()
	tpl, _ := s.Get("wasm-fast-lane")
	pol := tpl.ToPolicy()

	if pol.ExecutionClassRequired != protocol.ExecWasmSandbox {
		t.Errorf("ToPolicy: expected ExecWasmSandbox, got %q", pol.ExecutionClassRequired)
	}
	if !pol.SandboxRequired {
		t.Error("ToPolicy: expected SandboxRequired=true")
	}
	if pol.MaxRuntimeSec != 300 {
		t.Errorf("ToPolicy: expected MaxRuntimeSec 300, got %d", pol.MaxRuntimeSec)
	}
}

// ── ValidateExecutionClass with wasm ─────────────────────────────────────────

func TestValidateExecutionClass_Wasm(t *testing.T) {
	if err := ValidateExecutionClass(protocol.ExecWasmSandbox); err != nil {
		t.Errorf("expected ExecWasmSandbox to be valid, got: %v", err)
	}
}

func TestNewStore_TotalBuiltins_IncludesWasm(t *testing.T) {
	s := NewStore()
	list := s.List()
	// Previously >=3, now >=4 (observe-only, diagnose, full-remediate, wasm-fast-lane)
	if len(list) < 4 {
		t.Fatalf("expected at least 4 built-in templates, got %d", len(list))
	}
}
