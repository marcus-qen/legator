package sandbox

import (
	"strings"
	"testing"
)

func newTestArtifactStore(t *testing.T) (*Store, *ArtifactStore) {
	t.Helper()
	store := newTestStore(t)
	as, err := NewArtifactStore(store.DB())
	if err != nil {
		t.Fatalf("NewArtifactStore: %v", err)
	}
	return store, as
}

func makeArtifact(sandboxID, workspaceID, taskID, kind string, content []byte) *Artifact {
	return &Artifact{
		TaskID:      taskID,
		SandboxID:   sandboxID,
		WorkspaceID: workspaceID,
		Path:        "output/result.txt",
		Kind:        kind,
		MimeType:    "text/plain",
		Content:     content,
	}
}

// ── CRUD ──────────────────────────────────────────────────────────────────────

func TestArtifactStore_CreateAndGet(t *testing.T) {
	_, as := newTestArtifactStore(t)

	content := []byte("hello artifact")
	a := makeArtifact("sbx-1", "ws-1", "task-1", ArtifactKindFile, content)

	created, err := as.CreateArtifact(a)
	if err != nil {
		t.Fatalf("CreateArtifact: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if created.SHA256 == "" {
		t.Fatal("expected SHA256 to be set")
	}
	if created.Size != int64(len(content)) {
		t.Errorf("size: want %d got %d", len(content), created.Size)
	}

	// Fetch by ID.
	got, err := as.GetArtifact(created.ID, "ws-1")
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if got == nil {
		t.Fatal("expected artifact, got nil")
	}
	if string(got.Content) != "hello artifact" {
		t.Errorf("content mismatch: %q", got.Content)
	}
	if got.SHA256 != created.SHA256 {
		t.Errorf("SHA256 mismatch")
	}
}

func TestArtifactStore_List(t *testing.T) {
	_, as := newTestArtifactStore(t)

	for i := 0; i < 3; i++ {
		_, err := as.CreateArtifact(makeArtifact("sbx-1", "ws-1", "task-1", ArtifactKindFile, []byte("data")))
		if err != nil {
			t.Fatalf("CreateArtifact %d: %v", i, err)
		}
	}
	// Unrelated sandbox.
	_, _ = as.CreateArtifact(makeArtifact("sbx-2", "ws-1", "task-2", ArtifactKindFile, []byte("other")))

	arts, err := as.ListArtifacts(ArtifactListFilter{SandboxID: "sbx-1", WorkspaceID: "ws-1"})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(arts) != 3 {
		t.Errorf("want 3, got %d", len(arts))
	}
	// Content should NOT be returned in list.
	for _, a := range arts {
		if len(a.Content) > 0 {
			t.Errorf("expected no content in list, got %d bytes", len(a.Content))
		}
	}
}

func TestArtifactStore_ListFilterByTask(t *testing.T) {
	_, as := newTestArtifactStore(t)

	_, _ = as.CreateArtifact(makeArtifact("sbx-1", "ws-1", "task-A", ArtifactKindFile, []byte("a")))
	_, _ = as.CreateArtifact(makeArtifact("sbx-1", "ws-1", "task-B", ArtifactKindFile, []byte("b")))

	arts, err := as.ListArtifacts(ArtifactListFilter{SandboxID: "sbx-1", WorkspaceID: "ws-1", TaskID: "task-A"})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(arts) != 1 {
		t.Errorf("want 1, got %d", len(arts))
	}
}

func TestArtifactStore_WorkspaceIsolation(t *testing.T) {
	_, as := newTestArtifactStore(t)

	created, _ := as.CreateArtifact(makeArtifact("sbx-1", "ws-owner", "task-1", ArtifactKindFile, []byte("secret")))

	// Wrong workspace → nil.
	got, err := as.GetArtifact(created.ID, "ws-other")
	if err != nil {
		t.Fatalf("GetArtifact (wrong ws): %v", err)
	}
	if got != nil {
		t.Error("expected nil for wrong workspace, got artifact")
	}

	// Empty workspace → OK (admin bypass).
	got2, err := as.GetArtifact(created.ID, "")
	if err != nil {
		t.Fatalf("GetArtifact (empty ws): %v", err)
	}
	if got2 == nil {
		t.Error("expected artifact for empty workspace, got nil")
	}
}

func TestArtifactStore_DeleteArtifacts(t *testing.T) {
	_, as := newTestArtifactStore(t)

	for i := 0; i < 2; i++ {
		_, _ = as.CreateArtifact(makeArtifact("sbx-1", "ws-1", "task-1", ArtifactKindFile, []byte("x")))
	}

	if err := as.DeleteArtifacts("sbx-1"); err != nil {
		t.Fatalf("DeleteArtifacts: %v", err)
	}

	arts, _ := as.ListArtifacts(ArtifactListFilter{SandboxID: "sbx-1"})
	if len(arts) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(arts))
	}
}

// ── Size limits ───────────────────────────────────────────────────────────────

func TestArtifactStore_PerFileSizeLimit(t *testing.T) {
	_, as := newTestArtifactStore(t)

	huge := make([]byte, MaxArtifactSizeBytes+1)
	_, err := as.CreateArtifact(makeArtifact("sbx-1", "ws-1", "task-1", ArtifactKindFile, huge))
	if err == nil {
		t.Fatal("expected error for oversized artifact")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestArtifactStore_SandboxQuotaLimit(t *testing.T) {
	_, as := newTestArtifactStore(t)

	// Fill up to just below the limit.
	chunk := make([]byte, MaxArtifactSizeBytes) // 5 MB
	for i := 0; i < 10; i++ {
		_, err := as.CreateArtifact(makeArtifact("sbx-1", "ws-1", "task-1", ArtifactKindFile, chunk))
		if err != nil {
			// Quota hit — expected once we exceed 50 MB.
			if i < 10 && strings.Contains(err.Error(), "quota exceeded") {
				return // correct behaviour
			}
			t.Fatalf("unexpected error at i=%d: %v", i, err)
		}
	}
	// If we reach here, one more should fail.
	_, err := as.CreateArtifact(makeArtifact("sbx-1", "ws-1", "task-1", ArtifactKindFile, chunk))
	if err == nil {
		t.Fatal("expected quota error after 50 MB")
	}
	if !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ── Diff summary ──────────────────────────────────────────────────────────────

func TestArtifactStore_DiffSummaryComputed(t *testing.T) {
	_, as := newTestArtifactStore(t)

	diffContent := []byte(`--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 context
-old line
+new line
+another new line
 context2
`)

	a := makeArtifact("sbx-1", "ws-1", "task-1", ArtifactKindDiff, diffContent)
	created, err := as.CreateArtifact(a)
	if err != nil {
		t.Fatalf("CreateArtifact: %v", err)
	}
	if created.DiffSummary == "" {
		t.Error("expected diff_summary to be computed")
	}
	// Should be "+2 -1"
	if created.DiffSummary != "+2 -1" {
		t.Errorf("diff_summary: want '+2 -1', got %q", created.DiffSummary)
	}
}
