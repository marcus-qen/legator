package migration

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// BackupDatabase copies the SQLite file at dbPath to a timestamped backup file
// in the same directory, then verifies the backup passes PRAGMA integrity_check.
// Returns the path of the backup file.
func BackupDatabase(dbPath string) (string, error) {
	// Build backup path: {dir}/{base}.bak.{timestamp}
	dir := filepath.Dir(dbPath)
	base := filepath.Base(dbPath)
	ts := time.Now().UTC().Format(time.RFC3339)
	// Replace colons in RFC3339 timestamp so the filename is filesystem-safe.
	safeTS := strings.ReplaceAll(ts, ":", "-")
	backupPath := filepath.Join(dir, base+".bak."+safeTS)

	// Copy the file.
	if err := copyFile(dbPath, backupPath); err != nil {
		return "", fmt.Errorf("backup copy %s → %s: %w", dbPath, backupPath, err)
	}

	// Verify backup integrity.
	if err := checkIntegrity(backupPath); err != nil {
		// Remove the corrupt backup so we don't litter.
		_ = os.Remove(backupPath)
		return "", fmt.Errorf("backup integrity check failed for %s: %w", backupPath, err)
	}

	return backupPath, nil
}

// CleanOldBackups removes backup files for dbPath that are older than maxAge.
// Backup files are identified by the pattern {dbPath}.bak.* in the same directory.
func CleanOldBackups(dbPath string, maxAge time.Duration) error {
	dir := filepath.Dir(dbPath)
	base := filepath.Base(dbPath)
	pattern := filepath.Join(dir, base+".bak.*")

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob backups for %s: %w", dbPath, err)
	}

	cutoff := time.Now().Add(-maxAge)
	var errs []string
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			errs = append(errs, fmt.Sprintf("stat %s: %v", match, err))
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(match); err != nil {
				errs = append(errs, fmt.Sprintf("remove %s: %v", match, err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("clean old backups: %s", strings.Join(errs, "; "))
	}
	return nil
}

// copyFile copies src to dst using io.Copy.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create dest: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return out.Sync()
}

// checkIntegrity opens the SQLite file at path and runs PRAGMA integrity_check.
// Returns an error if the result is not "ok".
func checkIntegrity(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer db.Close()

	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return fmt.Errorf("integrity_check query: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check returned: %s", result)
	}
	return nil
}
