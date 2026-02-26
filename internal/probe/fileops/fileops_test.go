package fileops

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	logger, _ := zap.NewDevelopment()
	return logger
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

func expectedOwner(uid uint32) string {
	owner := strconv.FormatUint(uint64(uid), 10)
	if userInfo, err := user.LookupId(owner); err == nil {
		return userInfo.Username
	}
	return owner
}

func expectedGroup(gid uint32) string {
	group := strconv.FormatUint(uint64(gid), 10)
	if groupInfo, err := user.LookupGroupId(group); err == nil {
		return groupInfo.Name
	}
	return group
}

func TestReadFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	mustWriteFile(t, path, "hello-world")

	ops := New(Policy{}, testLogger())
	got, err := ops.ReadFile(path, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello-world" {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestReadFile_BlockedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocked.txt")
	mustWriteFile(t, path, "nope")

	ops := New(Policy{BlockedPaths: []string{path}}, testLogger())
	if _, err := ops.ReadFile(path, 0); err == nil {
		t.Fatal("expected blocked path error")
	}
}

func TestReadFile_TooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	mustWriteFile(t, path, strings.Repeat("x", 64))

	ops := New(Policy{MaxFileSize: 16}, testLogger())
	if _, err := ops.ReadFile(path, 0); err == nil {
		t.Fatal("expected too-large file error")
	}
}

func TestReadFile_BinaryDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.bin")
	binary := []byte{0x66, 0x6f, 0x00, 0x6f, 0x6f}
	if err := os.WriteFile(path, binary, 0o644); err != nil {
		t.Fatalf("write binary file: %v", err)
	}

	ops := New(Policy{}, testLogger())
	if _, err := ops.ReadFile(path, 0); err == nil {
		t.Fatal("expected binary file error")
	}
}

func TestSearchFiles_FindsMatches(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	mustWriteFile(t, filepath.Join(dir, "first.log"), "a")
	mustWriteFile(t, filepath.Join(dir, "second.txt"), "b")
	mustWriteFile(t, filepath.Join(dir, "nested", "third.log"), "c")

	ops := New(Policy{}, testLogger())
	results, err := ops.SearchFiles(dir, "*.log", 10)
	if err != nil {
		t.Fatalf("search error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestSearchFiles_RespectsMaxResults(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mustWriteFile(t, filepath.Join(dir, "one.log"), "a")
	mustWriteFile(t, filepath.Join(dir, "two.log"), "b")
	mustWriteFile(t, filepath.Join(dir, "three.log"), "c")
	mustWriteFile(t, filepath.Join(dir, "nested", "four.log"), "d")

	ops := New(Policy{}, testLogger())
	results, err := ops.SearchFiles(dir, "*.log", 2)
	if err != nil {
		t.Fatalf("search error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestSearchFiles_SkipsRestrictedRoots(t *testing.T) {
	dir := t.TempDir()
	dirs := []string{"proc", "sys", "dev", "run"}
	for _, d := range dirs {
		full := filepath.Join(dir, d)
		if err := os.Mkdir(full, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		mustWriteFile(t, filepath.Join(full, "skip.log"), "x")
	}
	mustWriteFile(t, filepath.Join(dir, "allowed.log"), "ok")

	ops := New(Policy{}, testLogger())
	results, err := ops.SearchFiles(dir, "skip.log", 10)
	if err != nil {
		t.Fatalf("search error: %v", err)
	}

	if len(results) != 0 {
		t.Fatalf("expected 0 matches from skipped directories, got %d", len(results))
	}

	results, err = ops.SearchFiles(dir, "allowed.log", 10)
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 allowed match, got %d", len(results))
	}
}

func TestStatFile_ReturnsInfo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.txt")
	content := "metadata"
	mustWriteFile(t, path, content)

	ops := New(Policy{}, testLogger())
	info, err := ops.StatFile(path)
	if err != nil {
		t.Fatalf("stat error: %v", err)
	}

	fsInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat (local) error: %v", err)
	}

	if info.Size != int64(len(content)) {
		t.Fatalf("expected size %d, got %d", len(content), info.Size)
	}
	if info.IsDir {
		t.Fatalf("expected file path to be not directory")
	}
	if !fsInfo.Mode().IsRegular() {
		t.Fatalf("expected regular file")
	}
	if info.Mode&fsInfo.Mode() != fsInfo.Mode() {
		t.Fatalf("mode mismatch: expected %v got %v", fsInfo.Mode(), info.Mode)
	}

	sysInfo := fsInfo.Sys().(*syscall.Stat_t)
	expectedOwner := expectedOwner(sysInfo.Uid)
	expectedGroup := expectedGroup(sysInfo.Gid)
	if info.Owner != expectedOwner {
		t.Fatalf("expected owner %q, got %q", expectedOwner, info.Owner)
	}
	if info.Group != expectedGroup {
		t.Fatalf("expected group %q, got %q", expectedGroup, info.Group)
	}

	expectedPath, _ := filepath.Abs(path)
	if info.Path != expectedPath {
		t.Fatalf("expected path %q, got %q", expectedPath, info.Path)
	}
}

func TestReadLines_RangeExtraction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.txt")
	mustWriteFile(t, path, "line1\nline2\nline3\nline4\nline5\n")

	ops := New(Policy{}, testLogger())
	lines, err := ops.ReadLines(path, 2, 3)
	if err != nil {
		t.Fatalf("read lines error: %v", err)
	}

	expected := []string{"line2", "line3", "line4"}
	if len(lines) != len(expected) {
		t.Fatalf("expected %d lines, got %d", len(expected), len(lines))
	}
	for i := range expected {
		if lines[i] != expected[i] {
			t.Fatalf("line %d mismatch: expected %q got %q", i, expected[i], lines[i])
		}
	}
}

func TestPolicy_PathCheckingAllowedAndBlocked(t *testing.T) {
	dir := t.TempDir()
	allowed := filepath.Join(dir, "allowed")
	blocked := filepath.Join(dir, "blocked")
	disallowed := filepath.Join(dir, "disallowed")
	if err := os.Mkdir(allowed, 0o755); err != nil {
		t.Fatalf("mkdir allowed: %v", err)
	}
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatalf("mkdir blocked: %v", err)
	}
	if err := os.Mkdir(disallowed, 0o755); err != nil {
		t.Fatalf("mkdir disallowed: %v", err)
	}

	allowedFile := filepath.Join(allowed, "ok.txt")
	blockedFile := filepath.Join(blocked, "bad.txt")
	disallowedFile := filepath.Join(disallowed, "other.txt")
	outside := filepath.Join(dir, "outside.txt")
	linkTarget := filepath.Join(dir, "link-target.txt")

	mustWriteFile(t, allowedFile, "allowed")
	mustWriteFile(t, blockedFile, "blocked")
	mustWriteFile(t, disallowedFile, "disallowed")
	mustWriteFile(t, outside, "outside")
	mustWriteFile(t, linkTarget, "linked")

	linkPath := filepath.Join(allowed, "link-to-outside")
	if err := os.Symlink(linkTarget, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	ops := New(Policy{AllowedPaths: []string{allowed}, BlockedPaths: []string{blockedFile}}, testLogger())

	if _, err := ops.ReadFile(allowedFile, 0); err != nil {
		t.Fatalf("allowed path should pass: %v", err)
	}
	if _, err := ops.ReadFile(blockedFile, 0); err == nil {
		t.Fatalf("blocked path should fail")
	}
	if _, err := ops.ReadFile(disallowedFile, 0); err == nil {
		t.Fatalf("disallowed path should fail")
	}
	if _, err := ops.ReadFile(linkPath, 0); err == nil {
		t.Fatalf("symlink outside allowed should fail")
	}
}
