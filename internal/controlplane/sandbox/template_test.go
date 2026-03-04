package sandbox

import (
	"testing"
	"time"
)

// ── TemplateStore helpers ─────────────────────────────────────────────────────

func newTestTemplateStore(t *testing.T) *TemplateStore {
	t.Helper()
	db := newTestStore(t).DB()
	ts, err := NewTemplateStore(db)
	if err != nil {
		t.Fatalf("NewTemplateStore: %v", err)
	}
	return ts
}

// ── Builtins ──────────────────────────────────────────────────────────────────

func TestTemplateStore_BuiltinsSeeded(t *testing.T) {
	ts := newTestTemplateStore(t)
	list, err := ts.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	ids := make(map[string]bool)
	for _, tpl := range list {
		ids[tpl.ID] = true
	}
	for _, want := range []string{"wasm-lint-check", "wasm-json-transform"} {
		if !ids[want] {
			t.Errorf("builtin template %q not found in List", want)
		}
	}
}

func TestTemplateStore_BuiltinsHaveWASMRuntime(t *testing.T) {
	ts := newTestTemplateStore(t)
	for _, id := range []string{"wasm-lint-check", "wasm-json-transform"} {
		tpl, err := ts.Get(id, "")
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		if tpl == nil {
			t.Fatalf("Get(%s): template not found", id)
		}
		if tpl.RuntimeClass != RuntimeClassWASM {
			t.Errorf("%s: expected runtime_class %q, got %q", id, RuntimeClassWASM, tpl.RuntimeClass)
		}
	}
}

func TestTemplateStore_BuiltinLintCheck(t *testing.T) {
	ts := newTestTemplateStore(t)
	tpl, err := ts.Get("wasm-lint-check", "")
	if err != nil || tpl == nil {
		t.Fatalf("Get wasm-lint-check: err=%v tpl=%v", err, tpl)
	}
	if tpl.CPUMillis != 250 {
		t.Errorf("expected cpu_millis=250, got %d", tpl.CPUMillis)
	}
	if tpl.MemoryMiB != 128 {
		t.Errorf("expected memory_mib=128, got %d", tpl.MemoryMiB)
	}
	if tpl.MaxRunSecs != 60 {
		t.Errorf("expected max_run_secs=60, got %d", tpl.MaxRunSecs)
	}
	if tpl.Metadata["task_kind"] != "lint" {
		t.Errorf("expected task_kind=lint, got %q", tpl.Metadata["task_kind"])
	}
}

func TestTemplateStore_BuiltinJSONTransform(t *testing.T) {
	ts := newTestTemplateStore(t)
	tpl, err := ts.Get("wasm-json-transform", "")
	if err != nil || tpl == nil {
		t.Fatalf("Get wasm-json-transform: err=%v tpl=%v", err, tpl)
	}
	if tpl.CPUMillis != 250 {
		t.Errorf("expected cpu_millis=250, got %d", tpl.CPUMillis)
	}
	if tpl.MemoryMiB != 64 {
		t.Errorf("expected memory_mib=64, got %d", tpl.MemoryMiB)
	}
	if tpl.Metadata["task_kind"] != "transform" {
		t.Errorf("expected task_kind=transform, got %q", tpl.Metadata["task_kind"])
	}
}

// ── CRUD ─────────────────────────────────────────────────────────────────────

func TestTemplateStore_Create_Get(t *testing.T) {
	ts := newTestTemplateStore(t)
	tpl := &SandboxTemplate{
		WorkspaceID:  "ws-1",
		Name:         "test-template",
		Description:  "A test template",
		RuntimeClass: RuntimeClassWASM,
		CPUMillis:    100,
		MemoryMiB:    32,
		MaxRunSecs:   20,
	}
	created, err := ts.Create(tpl)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected ID to be set")
	}
	got, err := ts.Get(created.ID, "ws-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected template, got nil")
	}
	if got.Name != "test-template" {
		t.Errorf("expected name test-template, got %q", got.Name)
	}
}

func TestTemplateStore_Create_GeneratesID(t *testing.T) {
	ts := newTestTemplateStore(t)
	tpl := &SandboxTemplate{Name: "auto-id", RuntimeClass: RuntimeClassKata}
	created, err := ts.Create(tpl)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected non-empty generated ID")
	}
}

func TestTemplateStore_Create_SetsTimestamps(t *testing.T) {
	ts := newTestTemplateStore(t)
	before := time.Now().Add(-time.Second)
	tpl := &SandboxTemplate{Name: "ts-test", RuntimeClass: RuntimeClassNative}
	created, err := ts.Create(tpl)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.CreatedAt.Before(before) {
		t.Errorf("created_at %v should be after %v", created.CreatedAt, before)
	}
}

func TestTemplateStore_Update(t *testing.T) {
	ts := newTestTemplateStore(t)
	tpl := &SandboxTemplate{Name: "old-name", RuntimeClass: RuntimeClassKata}
	created, _ := ts.Create(tpl)

	created.Name = "new-name"
	created.CPUMillis = 999
	if err := ts.Update(created); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := ts.Get(created.ID, "")
	if got.Name != "new-name" {
		t.Errorf("expected name new-name, got %q", got.Name)
	}
	if got.CPUMillis != 999 {
		t.Errorf("expected cpu_millis 999, got %d", got.CPUMillis)
	}
}

func TestTemplateStore_Delete(t *testing.T) {
	ts := newTestTemplateStore(t)
	tpl := &SandboxTemplate{Name: "to-delete", RuntimeClass: RuntimeClassNative}
	created, _ := ts.Create(tpl)

	if err := ts.Delete(created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := ts.Get(created.ID, "")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

func TestTemplateStore_Delete_NotFound(t *testing.T) {
	ts := newTestTemplateStore(t)
	if err := ts.Delete("nonexistent-id"); err == nil {
		t.Fatal("expected error for nonexistent ID")
	}
}

func TestTemplateStore_List_WorkspaceIsolation(t *testing.T) {
	ts := newTestTemplateStore(t)
	// Create a template for ws-a
	_, _ = ts.Create(&SandboxTemplate{WorkspaceID: "ws-a", Name: "a-template", RuntimeClass: RuntimeClassNative})
	// Create a template for ws-b
	_, _ = ts.Create(&SandboxTemplate{WorkspaceID: "ws-b", Name: "b-template", RuntimeClass: RuntimeClassNative})

	listA, _ := ts.List("ws-a")
	for _, tpl := range listA {
		if tpl.WorkspaceID == "ws-b" {
			t.Errorf("ws-a list contains ws-b template: %+v", tpl)
		}
	}

	listB, _ := ts.List("ws-b")
	for _, tpl := range listB {
		if tpl.WorkspaceID == "ws-a" {
			t.Errorf("ws-b list contains ws-a template: %+v", tpl)
		}
	}
}

func TestTemplateStore_List_BuiltinsVisibleToAll(t *testing.T) {
	ts := newTestTemplateStore(t)
	listA, _ := ts.List("ws-x")
	ids := make(map[string]bool)
	for _, tpl := range listA {
		ids[tpl.ID] = true
	}
	if !ids["wasm-lint-check"] {
		t.Error("builtin wasm-lint-check should be visible to workspace ws-x")
	}
}

func TestTemplateStore_Get_WorkspaceIsolation(t *testing.T) {
	ts := newTestTemplateStore(t)
	tpl := &SandboxTemplate{WorkspaceID: "ws-secret", Name: "secret", RuntimeClass: RuntimeClassNative}
	created, _ := ts.Create(tpl)

	// Other workspace should not see it
	got, err := ts.Get(created.ID, "ws-other")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for cross-workspace access, got %+v", got)
	}
}

func TestTemplateStore_ListByRuntimeClass(t *testing.T) {
	ts := newTestTemplateStore(t)
	_, _ = ts.Create(&SandboxTemplate{Name: "kata-1", RuntimeClass: RuntimeClassKata})
	list, err := ts.ListByRuntimeClass(RuntimeClassWASM, "")
	if err != nil {
		t.Fatalf("ListByRuntimeClass: %v", err)
	}
	for _, tpl := range list {
		if tpl.RuntimeClass != RuntimeClassWASM {
			t.Errorf("expected wasm templates only, got runtime_class=%q", tpl.RuntimeClass)
		}
	}
	// Built-ins are wasm, so list should contain at least 2
	if len(list) < 2 {
		t.Errorf("expected at least 2 wasm templates, got %d", len(list))
	}
}

// ── Capability validation ─────────────────────────────────────────────────────

func TestSandboxTemplate_ValidateCapabilities_WASMRejectsNetwork(t *testing.T) {
	tpl := &SandboxTemplate{
		RuntimeClass: RuntimeClassWASM,
		Capabilities: []string{CapNetworkAccess},
	}
	if err := tpl.ValidateCapabilities(); err == nil {
		t.Fatal("expected error for network capability in WASM lane")
	}
}

func TestSandboxTemplate_ValidateCapabilities_WASMRejectsHostFSWrite(t *testing.T) {
	tpl := &SandboxTemplate{
		RuntimeClass: RuntimeClassWASM,
		Capabilities: []string{CapHostFSWrite},
	}
	if err := tpl.ValidateCapabilities(); err == nil {
		t.Fatal("expected error for host_fs_write in WASM lane")
	}
}

func TestSandboxTemplate_ValidateCapabilities_WASMAllowsHostFSRead(t *testing.T) {
	tpl := &SandboxTemplate{
		RuntimeClass: RuntimeClassWASM,
		Capabilities: []string{CapHostFSRead},
	}
	if err := tpl.ValidateCapabilities(); err != nil {
		t.Errorf("expected host_fs_read to be permitted in WASM lane, got: %v", err)
	}
}

func TestSandboxTemplate_ValidateCapabilities_KataAllowsNetwork(t *testing.T) {
	tpl := &SandboxTemplate{
		RuntimeClass: RuntimeClassKata,
		Capabilities: []string{CapNetworkAccess},
	}
	if err := tpl.ValidateCapabilities(); err != nil {
		t.Errorf("expected network to be allowed for kata lane: %v", err)
	}
}

func TestTemplateStore_Create_RejectsInvalidWASMCaps(t *testing.T) {
	ts := newTestTemplateStore(t)
	tpl := &SandboxTemplate{
		Name:         "bad-wasm",
		RuntimeClass: RuntimeClassWASM,
		Capabilities: []string{CapNetworkAccess},
	}
	_, err := ts.Create(tpl)
	if err == nil {
		t.Fatal("expected error when creating WASM template with network capability")
	}
}

// ── WASM lane validators ──────────────────────────────────────────────────────

func TestValidateWASMLaneSession_Valid(t *testing.T) {
	if err := ValidateWASMLaneSession(RuntimeClassWASM, false); err != nil {
		t.Errorf("expected valid WASM session, got: %v", err)
	}
}

func TestValidateWASMLaneSession_WrongRuntime(t *testing.T) {
	if err := ValidateWASMLaneSession(RuntimeClassKata, false); err == nil {
		t.Fatal("expected error for wrong runtime class")
	}
}

func TestValidateWASMLaneSession_HostDirectDenied(t *testing.T) {
	if err := ValidateWASMLaneSession(RuntimeClassWASM, true); err == nil {
		t.Fatal("expected error when host-direct is allowed in WASM lane")
	}
}

func TestIsWASMDeniedOperation(t *testing.T) {
	tests := []struct {
		op   string
		want bool
	}{
		{"network_egress", true},
		{"host_fs_write", true},
		{"mount", true},
		{"exec_host_process", true},
		{"raw_socket", true},
		{"read_file", false},
		{"write_output", false},
	}
	for _, tt := range tests {
		got := IsWASMDeniedOperation(tt.op)
		if got != tt.want {
			t.Errorf("IsWASMDeniedOperation(%q) = %v, want %v", tt.op, got, tt.want)
		}
	}
}

func TestWASMDeniedOperations_NotEmpty(t *testing.T) {
	ops := WASMDeniedOperations()
	if len(ops) == 0 {
		t.Fatal("WASMDeniedOperations must not be empty")
	}
}
