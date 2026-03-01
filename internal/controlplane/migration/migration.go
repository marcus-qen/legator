// Package migration provides SQLite schema versioning, migration running,
// database backup, and downgrade protection for Legator stores.
package migration

import (
	"database/sql"
	"fmt"
	"time"
)

// SchemaVersion records the schema version applied to a SQLite database.
type SchemaVersion struct {
	StoreName string
	Version   int
	AppliedAt time.Time
}

const createVersionTable = `
CREATE TABLE IF NOT EXISTS _schema_version (
	store_name TEXT NOT NULL DEFAULT '',
	version    INTEGER NOT NULL DEFAULT 0,
	applied_at TEXT NOT NULL
)`

// ensureTable creates the _schema_version table if it doesn't exist.
func ensureTable(db *sql.DB) error {
	if _, err := db.Exec(createVersionTable); err != nil {
		return fmt.Errorf("create _schema_version: %w", err)
	}
	return nil
}

// CurrentVersion returns the current schema version stored in db.
// Returns 0 if the _schema_version table does not exist or is empty.
func CurrentVersion(db *sql.DB) (int, error) {
	// Check if the table exists first.
	var name string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='_schema_version'`,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("check _schema_version table: %w", err)
	}

	var version int
	err = db.QueryRow(`SELECT version FROM _schema_version LIMIT 1`).Scan(&version)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

// SetVersion inserts or updates the schema version in db.
func SetVersion(db *sql.DB, version int) error {
	if err := ensureTable(db); err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Try update first.
	res, err := db.Exec(`UPDATE _schema_version SET version = ?, applied_at = ?`, version, now)
	if err != nil {
		return fmt.Errorf("update schema version: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows > 0 {
		return nil
	}

	// No row exists yet — insert.
	if _, err := db.Exec(
		`INSERT INTO _schema_version (store_name, version, applied_at) VALUES ('', ?, ?)`,
		version, now,
	); err != nil {
		return fmt.Errorf("insert schema version: %w", err)
	}
	return nil
}

// NeedsMigration reports whether the current schema version is below targetVersion.
func NeedsMigration(db *sql.DB, targetVersion int) (bool, error) {
	current, err := CurrentVersion(db)
	if err != nil {
		return false, err
	}
	return current < targetVersion, nil
}

// EnsureVersion creates the _schema_version table if needed and sets the
// version to initialVersion only if no version has been recorded yet.
// It is idempotent and safe to call on every startup.
func EnsureVersion(db *sql.DB, initialVersion int) error {
	if err := ensureTable(db); err != nil {
		return err
	}

	current, err := CurrentVersion(db)
	if err != nil {
		return err
	}
	if current != 0 {
		// Version already set — leave it alone.
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO _schema_version (store_name, version, applied_at) VALUES ('', ?, ?)`,
		initialVersion, now,
	); err != nil {
		return fmt.Errorf("set initial schema version: %w", err)
	}
	return nil
}

// CheckVersion returns an error if the schema version stored in db is newer
// than binaryVersion. Call this during server startup to prevent running an
// old binary against a newer schema.
func CheckVersion(db *sql.DB, binaryVersion int) error {
	current, err := CurrentVersion(db)
	if err != nil {
		return err
	}
	if current > binaryVersion {
		return fmt.Errorf(
			"database schema version %d is newer than binary version %d — "+
				"refusing to start (use a newer binary or restore from backup)",
			current, binaryVersion,
		)
	}
	return nil
}
