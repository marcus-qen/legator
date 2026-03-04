package sandbox

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── Lane resolver tests ───────────────────────────────────────────────────────

func TestResolveLane_Wasm(t *testing.T) {
	ld, err := ResolveLane(LaneWasm)
	if err != nil {
		t.Fatalf("ResolveLane(wasm) error: %v", err)
	}
	if ld.RuntimeClass != "wasmtime" {
		t.Errorf("expected runtime_class wasmtime, got %q", ld.RuntimeClass)
	}
	if ld.TemplateID != "wasm-fast-lane" {
		t.Errorf("expected template_id wasm-fast-lane, got %q", ld.TemplateID)
	}
	if ld.CPUMillis != 500 {
		t.Errorf("expected cpu_millis 500, got %d", ld.CPUMillis)
	}
	if ld.MemoryMiB != 256 {
		t.Errorf("expected memory_mib 256, got %d", ld.MemoryMiB)
	}
	if ld.HostDirectAllowed {
		t.Error("expected HostDirectAllowed=false for wasm lane")
	}
}

func TestResolveLane_Container(t *testing.T) {
	ld, err := ResolveLane(LaneContainer)
	if err != nil {
		t.Fatalf("ResolveLane(container) error: %v", err)
	}
	if ld.RuntimeClass != "runc" {
		t.Errorf("expected runtime_class runc, got %q", ld.RuntimeClass)
	}
	if ld.HostDirectAllowed {
		t.Error("expected HostDirectAllowed=false for container lane")
	}
}

func TestResolveLane_Kata(t *testing.T) {
	ld, err := ResolveLane(LaneKata)
	if err != nil {
		t.Fatalf("ResolveLane(kata) error: %v", err)
	}
	if ld.RuntimeClass != "kata-containers" {
		t.Errorf("expected runtime_class kata-containers, got %q", ld.RuntimeClass)
	}
	if ld.HostDirectAllowed {
		t.Error("expected HostDirectAllowed=false for kata lane")
	}
}

func TestResolveLane_Empty_NoDefaults(t *testing.T) {
	ld, err := ResolveLane("")
	if err != nil {
		t.Fatalf("ResolveLane('') should not error, got: %v", err)
	}
	if ld.RuntimeClass != "" || ld.TemplateID != "" {
		t.Errorf("expected empty defaults for empty lane, got %+v", ld)
	}
}

func TestResolveLane_Unknown_Rejected(t *testing.T) {
	_, err := ResolveLane("hypervisor-x")
	if err == nil {
		t.Fatal("expected error for unknown lane")
	}
}

func TestKnownLanes_ContainsAll(t *testing.T) {
	lanes := KnownLanes()
	must := map[string]bool{LaneContainer: false, LaneKata: false, LaneWasm: false}
	for _, l := range lanes {
		must[l] = true
	}
	for name, found := range must {
		if !found {
			t.Errorf("lane %q missing from KnownLanes: %v", name, lanes)
		}
	}
}

// ── Handler integration tests for lane=wasm ───────────────────────────────────

func TestHandleCreate_WasmLane_SetsRuntimeClass(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)

	body, _ := json.Marshal(map[string]any{
		"workspace_id": "ws-wasm",
		"probe_id":     "probe-wasm",
		"lane":         "wasm",
		"created_by":   "alice",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", bytes.NewBuffer(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var sess SandboxSession
	if err := json.NewDecoder(w.Body).Decode(&sess); err != nil {
		t.Fatal(err)
	}
	if sess.RuntimeClass != "wasmtime" {
		t.Errorf("expected runtime_class wasmtime, got %q", sess.RuntimeClass)
	}
	if sess.TemplateID != "wasm-fast-lane" {
		t.Errorf("expected template_id wasm-fast-lane, got %q", sess.TemplateID)
	}
	if sess.Metadata["lane"] != "wasm" {
		t.Errorf("expected metadata lane=wasm, got %q", sess.Metadata["lane"])
	}
	if sess.Metadata["lane_cpu_millis"] != "500" {
		t.Errorf("expected lane_cpu_millis=500, got %q", sess.Metadata["lane_cpu_millis"])
	}
	if sess.Metadata["lane_memory_mib"] != "256" {
		t.Errorf("expected lane_memory_mib=256, got %q", sess.Metadata["lane_memory_mib"])
	}
}

func TestHandleCreate_WasmLane_ResourceDefaults(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)

	body, _ := json.Marshal(map[string]any{
		"workspace_id": "ws-wasm",
		"lane":         "wasm",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", bytes.NewBuffer(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var sess SandboxSession
	json.NewDecoder(w.Body).Decode(&sess)
	// Defaults from wasm lane
	if sess.Metadata["lane_cpu_millis"] != "500" {
		t.Errorf("expected lane_cpu_millis 500, got %q", sess.Metadata["lane_cpu_millis"])
	}
	if sess.Metadata["lane_memory_mib"] != "256" {
		t.Errorf("expected lane_memory_mib 256, got %q", sess.Metadata["lane_memory_mib"])
	}
}

func TestHandleCreate_UnknownLane_Rejected(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)

	body, _ := json.Marshal(map[string]any{
		"workspace_id": "ws-test",
		"lane":         "turbo-hypervisor",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", bytes.NewBuffer(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown lane, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["code"] != "invalid_lane" {
		t.Errorf("expected error code invalid_lane, got %q", resp["code"])
	}
}

func TestHandleCreate_WasmLane_ExplicitRuntimeClassOverrides(t *testing.T) {
	h := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sandboxes", h.HandleCreate)

	body, _ := json.Marshal(map[string]any{
		"workspace_id":  "ws-wasm",
		"lane":          "wasm",
		"runtime_class": "wasmtime-custom",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandboxes", bytes.NewBuffer(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var sess SandboxSession
	json.NewDecoder(w.Body).Decode(&sess)
	// Explicit runtime_class should NOT be overridden by lane default
	if sess.RuntimeClass != "wasmtime-custom" {
		t.Errorf("expected explicit runtime_class wasmtime-custom preserved, got %q", sess.RuntimeClass)
	}
}
