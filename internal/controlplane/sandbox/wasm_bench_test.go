package sandbox

import (
	"fmt"
	"testing"
)

// ── Benchmark helpers ─────────────────────────────────────────────────────────

func benchmarkTemplateStore(b *testing.B) *TemplateStore {
	b.Helper()
	// Use a shared in-memory store per benchmark to avoid per-op OS syscalls.
	db := newTestStore(&testing.T{}).DB()
	ts, err := NewTemplateStore(db)
	if err != nil {
		b.Fatalf("NewTemplateStore: %v", err)
	}
	return ts
}

// BenchmarkTemplateInstantiation_WASM measures the time to Create a WASM
// template in the store (simulating fast-lane template provisioning).
func BenchmarkTemplateInstantiation_WASM(b *testing.B) {
	ts := benchmarkTemplateStore(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tpl := &SandboxTemplate{
			WorkspaceID:  "bench-ws",
			Name:         fmt.Sprintf("bench-wasm-%d", i),
			RuntimeClass: RuntimeClassWASM,
			CPUMillis:    WASMCPUCapMillis,
			MemoryMiB:    WASMMemoryCapMiB,
			MaxRunSecs:   WASMMaxRunSecs,
		}
		if _, err := ts.Create(tpl); err != nil {
			b.Fatalf("Create: %v", err)
		}
	}
}

// BenchmarkTemplateInstantiation_Kata measures the time to Create a kata
// template for comparison against WASM fast lane overhead.
func BenchmarkTemplateInstantiation_Kata(b *testing.B) {
	ts := benchmarkTemplateStore(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tpl := &SandboxTemplate{
			WorkspaceID:  "bench-ws",
			Name:         fmt.Sprintf("bench-kata-%d", i),
			RuntimeClass: RuntimeClassKata,
			CPUMillis:    2000,
			MemoryMiB:    1024,
			MaxRunSecs:   600,
			Capabilities: []string{CapNetworkAccess, CapProcessSpawn},
		}
		if _, err := ts.Create(tpl); err != nil {
			b.Fatalf("Create: %v", err)
		}
	}
}

// BenchmarkTemplateGet_WASM measures the hot-path read latency for a WASM
// built-in template (simulating fast-lane session startup lookup).
func BenchmarkTemplateGet_WASM(b *testing.B) {
	ts := benchmarkTemplateStore(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ts.Get("wasm-lint-check", ""); err != nil {
			b.Fatalf("Get: %v", err)
		}
	}
}

// BenchmarkTemplateGet_Kata measures get latency for a kata template.
func BenchmarkTemplateGet_Kata(b *testing.B) {
	ts := benchmarkTemplateStore(b)
	// Seed a kata template
	kata := &SandboxTemplate{
		ID:           "bench-kata-get",
		Name:         "bench-kata-get",
		RuntimeClass: RuntimeClassKata,
		CPUMillis:    2000,
		MemoryMiB:    1024,
	}
	if _, err := ts.Create(kata); err != nil {
		b.Fatalf("seed kata: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ts.Get("bench-kata-get", ""); err != nil {
			b.Fatalf("Get: %v", err)
		}
	}
}

// BenchmarkTemplateList_WASM measures the cost of listing WASM templates
// (simulating fast-lane scheduling decisions that enumerate available runtimes).
func BenchmarkTemplateList_WASM(b *testing.B) {
	ts := benchmarkTemplateStore(b)
	// Pre-populate 10 WASM templates
	for i := 0; i < 10; i++ {
		_, _ = ts.Create(&SandboxTemplate{
			WorkspaceID:  "bench-ws",
			Name:         fmt.Sprintf("wasm-%d", i),
			RuntimeClass: RuntimeClassWASM,
			CPUMillis:    WASMCPUCapMillis,
			MemoryMiB:    WASMMemoryCapMiB,
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ts.ListByRuntimeClass(RuntimeClassWASM, "bench-ws"); err != nil {
			b.Fatalf("ListByRuntimeClass: %v", err)
		}
	}
}

// BenchmarkTemplateList_Kata provides an equivalent list benchmark for kata,
// enabling apples-to-apples comparison of store overhead per runtime class.
func BenchmarkTemplateList_Kata(b *testing.B) {
	ts := benchmarkTemplateStore(b)
	for i := 0; i < 10; i++ {
		_, _ = ts.Create(&SandboxTemplate{
			WorkspaceID:  "bench-ws",
			Name:         fmt.Sprintf("kata-%d", i),
			RuntimeClass: RuntimeClassKata,
			CPUMillis:    2000,
			MemoryMiB:    1024,
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ts.ListByRuntimeClass(RuntimeClassKata, "bench-ws"); err != nil {
			b.Fatalf("ListByRuntimeClass: %v", err)
		}
	}
}

// BenchmarkCapabilityValidation_WASM measures inline capability validation cost.
func BenchmarkCapabilityValidation_WASM(b *testing.B) {
	tpl := &SandboxTemplate{
		RuntimeClass: RuntimeClassWASM,
		Capabilities: []string{CapHostFSRead},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := tpl.ValidateCapabilities(); err != nil {
			b.Fatalf("unexpected validation error: %v", err)
		}
	}
}

// BenchmarkCapabilityValidation_Kata provides comparison for kata (no WASM checks).
func BenchmarkCapabilityValidation_Kata(b *testing.B) {
	tpl := &SandboxTemplate{
		RuntimeClass: RuntimeClassKata,
		Capabilities: []string{CapNetworkAccess, CapProcessSpawn},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := tpl.ValidateCapabilities(); err != nil {
			b.Fatalf("unexpected validation error: %v", err)
		}
	}
}

// BenchmarkIsWASMDeniedOperation exercises the hot-path policy deny check.
func BenchmarkIsWASMDeniedOperation(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsWASMDeniedOperation("network_egress")
	}
}

// BenchmarkIsWASMDeniedOperation_Allow measures the allowed-operation case.
func BenchmarkIsWASMDeniedOperation_Allow(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = IsWASMDeniedOperation("read_file")
	}
}
