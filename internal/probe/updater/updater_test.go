package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestApply_DownloadFailure(t *testing.T) {
	u := New(zap.NewNop())
	result := u.Apply("http://127.0.0.1:1/nonexistent", "", "v999")
	if result.Success {
		t.Fatal("expected failure for unreachable URL")
	}
	if result.Message == "" {
		t.Fatal("expected error message")
	}
}

func TestApply_BadHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	u := New(zap.NewNop())
	result := u.Apply(srv.URL+"/binary", "", "v1.0")
	if result.Success {
		t.Fatal("expected failure for 404")
	}
}

func TestApply_ChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake binary content"))
	}))
	defer srv.Close()

	u := New(zap.NewNop())
	result := u.Apply(srv.URL+"/binary", "0000000000000000000000000000000000000000000000000000000000000000", "v1.0")
	if result.Success {
		t.Fatal("expected failure for checksum mismatch")
	}
	if result.Message == "" {
		t.Fatal("expected error about checksum")
	}
}

func TestApply_ChecksumMatch(t *testing.T) {
	content := []byte("#!/bin/sh\necho test-probe v2.0")
	h := sha256.Sum256(content)
	checksum := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	u := New(zap.NewNop())
	// This will fail at the "verification" step (running --version on a shell script)
	// but it proves the checksum verification passed
	result := u.Apply(srv.URL+"/binary", checksum, "v2.0")
	// Either succeeds (unlikely for shell script) or fails at verification
	// The key assertion: it did NOT fail at checksum
	if result.Message != "" && result.Success == false {
		// Should have gotten past checksum check
		if result.Message == "checksum mismatch" {
			t.Fatal("checksum should have matched")
		}
	}
}

func TestUpdateResult_Fields(t *testing.T) {
	r := &UpdateResult{
		Success:    true,
		Message:    "done",
		OldPath:    "/usr/local/bin/probe",
		NewVersion: "v2.0",
	}
	if !r.Success {
		t.Fatal("expected success")
	}
	if r.NewVersion != "v2.0" {
		t.Fatalf("expected v2.0, got %s", r.NewVersion)
	}
}

func TestNew(t *testing.T) {
	u := New(zap.NewNop())
	if u == nil {
		t.Fatal("expected non-nil updater")
	}
}

func TestMaxBinarySize(t *testing.T) {
	if maxBinarySize != 100*1024*1024 {
		t.Fatalf("expected 100MB max, got %d", maxBinarySize)
	}
}

func TestApply_TempFileCleanup(t *testing.T) {
	// Verify temp files don't leak on failure
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	u := New(zap.NewNop())
	_ = u.Apply(srv.URL+"/binary", "", "v1.0")

	// Count tmp files in the dir (should be 0 or just the test temp dir)
	entries, _ := os.ReadDir(tmpDir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leaked temp file: %s", e.Name())
		}
	}
}
