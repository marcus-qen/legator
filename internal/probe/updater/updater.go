// Package updater handles probe binary self-update.
// On receiving an update command, the probe downloads the new binary,
// verifies its SHA256 checksum, atomically swaps the executable, and
// optionally restarts the service.
package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"go.uber.org/zap"
)

const (
	downloadTimeout = 5 * time.Minute
	maxBinarySize   = 100 * 1024 * 1024 // 100MB max
)

// Updater downloads and installs new probe binaries.
type Updater struct {
	logger *zap.Logger
}

// New creates a new Updater.
func New(logger *zap.Logger) *Updater {
	return &Updater{logger: logger}
}

// UpdateResult contains the result of an update attempt.
type UpdateResult struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	OldPath    string `json:"old_path,omitempty"`
	NewVersion string `json:"new_version,omitempty"`
}

// Apply downloads the binary from url, verifies sha256 checksum, and
// atomically replaces the current executable. Returns the result.
func (u *Updater) Apply(url, checksum, version string) *UpdateResult {
	u.logger.Info("starting self-update",
		zap.String("url", url),
		zap.String("version", version),
	)

	// Get current executable path
	exePath, err := os.Executable()
	if err != nil {
		return &UpdateResult{Message: fmt.Sprintf("cannot locate executable: %v", err)}
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return &UpdateResult{Message: fmt.Sprintf("cannot resolve symlinks: %v", err)}
	}

	// Download to temp file in same directory (for atomic rename)
	dir := filepath.Dir(exePath)
	tmpFile, err := os.CreateTemp(dir, "probe-update-*.tmp")
	if err != nil {
		return &UpdateResult{Message: fmt.Sprintf("cannot create temp file: %v", err)}
	}
	tmpPath := tmpFile.Name()
	defer func() {
		// Clean up temp file on failure
		os.Remove(tmpPath)
	}()

	// Download
	client := &http.Client{Timeout: downloadTimeout}
	resp, err := client.Get(url)
	if err != nil {
		tmpFile.Close()
		return &UpdateResult{Message: fmt.Sprintf("download failed: %v", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmpFile.Close()
		return &UpdateResult{Message: fmt.Sprintf("download returned HTTP %d", resp.StatusCode)}
	}

	// Download with size limit and checksum
	hasher := sha256.New()
	writer := io.MultiWriter(tmpFile, hasher)
	n, err := io.Copy(writer, io.LimitReader(resp.Body, maxBinarySize))
	tmpFile.Close()
	if err != nil {
		return &UpdateResult{Message: fmt.Sprintf("download write failed: %v", err)}
	}

	u.logger.Info("download complete", zap.Int64("bytes", n))

	// Verify checksum
	gotChecksum := hex.EncodeToString(hasher.Sum(nil))
	if checksum != "" && gotChecksum != checksum {
		return &UpdateResult{
			Message: fmt.Sprintf("checksum mismatch: expected %s, got %s", checksum, gotChecksum),
		}
	}

	// Make executable
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return &UpdateResult{Message: fmt.Sprintf("chmod failed: %v", err)}
	}

	// Quick sanity: run --version on the new binary
	out, err := exec.Command(tmpPath, "version").CombinedOutput()
	if err != nil {
		return &UpdateResult{Message: fmt.Sprintf("new binary verification failed: %v: %s", err, string(out))}
	}
	u.logger.Info("new binary verified", zap.String("output", string(out)))

	// Atomic swap: rename temp → current exe
	// On Linux, renaming an open executable works (the kernel keeps the old inode)
	if runtime.GOOS == "windows" {
		// Windows can't rename running executables, rename old first
		backupPath := exePath + ".old"
		os.Remove(backupPath)
		if err := os.Rename(exePath, backupPath); err != nil {
			return &UpdateResult{Message: fmt.Sprintf("backup rename failed: %v", err)}
		}
	}

	if err := os.Rename(tmpPath, exePath); err != nil {
		return &UpdateResult{Message: fmt.Sprintf("swap failed: %v", err)}
	}

	u.logger.Info("binary swapped successfully",
		zap.String("path", exePath),
		zap.String("version", version),
		zap.String("checksum", gotChecksum),
	)

	return &UpdateResult{
		Success:    true,
		Message:    fmt.Sprintf("updated to %s (sha256:%s)", version, gotChecksum[:12]),
		OldPath:    exePath,
		NewVersion: version,
	}
}

// Restart restarts the probe service via systemd.
func (u *Updater) Restart() error {
	u.logger.Info("restarting probe service")
	cmd := exec.Command("systemctl", "restart", "legator-probe")
	return cmd.Start() // Don't wait — we're the process being restarted
}
