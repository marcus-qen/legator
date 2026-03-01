package migration_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/migration"
	_ "modernc.org/sqlite"
)

// openTempDB creates an in-memory (or temp-file) SQLite database for testing.
func openTempDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// openTempFileDB creates a real SQLite file in t.TempDir() for tests that need
// a file path (backup tests).
func openTempFileDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite file: %v", err)
	}
	// Initialise with a trivial table so the file is written.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS _init (x INTEGER)`); err != nil {
		t.Fatalf("init table: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

// ---------------------------------------------------------------------------
// Schema version tests
// ---------------------------------------------------------------------------

func TestCurrentVersion_FreshDB(t *testing.T) {
	db := openTempDB(t)
	v, err := migration.CurrentVersion(db)
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	if v != 0 {
		t.Errorf("want 0, got %d", v)
	}
}

func TestSetAndCurrentVersion(t *testing.T) {
	db := openTempDB(t)

	if err := migration.SetVersion(db, 3); err != nil {
		t.Fatalf("SetVersion(3): %v", err)
	}
	v, err := migration.CurrentVersion(db)
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	if v != 3 {
		t.Errorf("want 3, got %d", v)
	}

	// Update to 7.
	if err := migration.SetVersion(db, 7); err != nil {
		t.Fatalf("SetVersion(7): %v", err)
	}
	v, _ = migration.CurrentVersion(db)
	if v != 7 {
		t.Errorf("want 7 after update, got %d", v)
	}
}

func TestNeedsMigration(t *testing.T) {
	db := openTempDB(t)
	_ = migration.SetVersion(db, 2)

	needs, err := migration.NeedsMigration(db, 5)
	if err != nil {
		t.Fatalf("NeedsMigration: %v", err)
	}
	if !needs {
		t.Error("expected needs=true when current(2) < target(5)")
	}

	needs, err = migration.NeedsMigration(db, 2)
	if err != nil {
		t.Fatalf("NeedsMigration: %v", err)
	}
	if needs {
		t.Error("expected needs=false when current==target")
	}

	needs, err = migration.NeedsMigration(db, 1)
	if err != nil {
		t.Fatalf("NeedsMigration: %v", err)
	}
	if needs {
		t.Error("expected needs=false when current(2) > target(1)")
	}
}

// ---------------------------------------------------------------------------
// EnsureVersion tests
// ---------------------------------------------------------------------------

func TestEnsureVersion_SetOnFreshDB(t *testing.T) {
	db := openTempDB(t)
	if err := migration.EnsureVersion(db, 1); err != nil {
		t.Fatalf("EnsureVersion: %v", err)
	}
	v, _ := migration.CurrentVersion(db)
	if v != 1 {
		t.Errorf("want 1, got %d", v)
	}
}

func TestEnsureVersion_Idempotent(t *testing.T) {
	db := openTempDB(t)
	if err := migration.EnsureVersion(db, 1); err != nil {
		t.Fatalf("first EnsureVersion: %v", err)
	}
	if err := migration.EnsureVersion(db, 1); err != nil {
		t.Fatalf("second EnsureVersion: %v", err)
	}
	v, _ := migration.CurrentVersion(db)
	if v != 1 {
		t.Errorf("want 1 after double call, got %d", v)
	}
}

func TestEnsureVersion_DoesNotOverwrite(t *testing.T) {
	db := openTempDB(t)
	// Manually set version to 5.
	if err := migration.SetVersion(db, 5); err != nil {
		t.Fatalf("SetVersion(5): %v", err)
	}
	// EnsureVersion with 1 should leave it at 5.
	if err := migration.EnsureVersion(db, 1); err != nil {
		t.Fatalf("EnsureVersion: %v", err)
	}
	v, _ := migration.CurrentVersion(db)
	if v != 5 {
		t.Errorf("want 5 (unchanged), got %d", v)
	}
}

// ---------------------------------------------------------------------------
// Migration runner tests
// ---------------------------------------------------------------------------

func testMigrations() []migration.Migration {
	return []migration.Migration{
		{
			Version:     1,
			Description: "create test_table_a",
			Up: func(tx *sql.Tx) error {
				_, err := tx.Exec(`CREATE TABLE test_table_a (id INTEGER PRIMARY KEY, name TEXT)`)
				return err
			},
			Down: func(tx *sql.Tx) error {
				_, err := tx.Exec(`DROP TABLE IF EXISTS test_table_a`)
				return err
			},
		},
		{
			Version:     2,
			Description: "add column age to test_table_a",
			Up: func(tx *sql.Tx) error {
				_, err := tx.Exec(`ALTER TABLE test_table_a ADD COLUMN age INTEGER DEFAULT 0`)
				return err
			},
			Down: func(tx *sql.Tx) error {
				// SQLite doesn't support DROP COLUMN in older versions; recreate.
				_, err := tx.Exec(`CREATE TABLE test_table_a_new (id INTEGER PRIMARY KEY, name TEXT)`)
				if err != nil {
					return err
				}
				if _, err = tx.Exec(`INSERT INTO test_table_a_new SELECT id, name FROM test_table_a`); err != nil {
					return err
				}
				if _, err = tx.Exec(`DROP TABLE test_table_a`); err != nil {
					return err
				}
				_, err = tx.Exec(`ALTER TABLE test_table_a_new RENAME TO test_table_a`)
				return err
			},
		},
		{
			Version:     3,
			Description: "create test_table_b",
			Up: func(tx *sql.Tx) error {
				_, err := tx.Exec(`CREATE TABLE test_table_b (id INTEGER PRIMARY KEY, value TEXT)`)
				return err
			},
			Down: func(tx *sql.Tx) error {
				_, err := tx.Exec(`DROP TABLE IF EXISTS test_table_b`)
				return err
			},
		},
	}
}

func tableExists(db *sql.DB, name string) bool {
	var n string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name,
	).Scan(&n)
	return err == nil
}

func columnExists(db *sql.DB, table, column string) bool {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var cname, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &cname, &ctype, &notnull, &dflt, &pk); err != nil {
			continue
		}
		if cname == column {
			return true
		}
	}
	return false
}

func TestRunner_MigrateForward(t *testing.T) {
	db := openTempDB(t)
	r := migration.NewRunner("test", testMigrations())

	if err := r.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	v, _ := migration.CurrentVersion(db)
	if v != 3 {
		t.Errorf("want schema v3, got %d", v)
	}
	if !tableExists(db, "test_table_a") {
		t.Error("test_table_a should exist")
	}
	if !columnExists(db, "test_table_a", "age") {
		t.Error("age column should exist in test_table_a")
	}
	if !tableExists(db, "test_table_b") {
		t.Error("test_table_b should exist")
	}
}

func TestRunner_MigrateTo(t *testing.T) {
	db := openTempDB(t)
	r := migration.NewRunner("test", testMigrations())

	if err := r.MigrateTo(db, 2); err != nil {
		t.Fatalf("MigrateTo(2): %v", err)
	}

	v, _ := migration.CurrentVersion(db)
	if v != 2 {
		t.Errorf("want schema v2, got %d", v)
	}
	if !tableExists(db, "test_table_a") {
		t.Error("test_table_a should exist")
	}
	if tableExists(db, "test_table_b") {
		t.Error("test_table_b should NOT exist at v2")
	}
}

func TestRunner_Rollback(t *testing.T) {
	db := openTempDB(t)
	r := migration.NewRunner("test", testMigrations())

	// Migrate to v3 first.
	if err := r.Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Roll back to v2 (removes test_table_b).
	if err := r.Rollback(db, 2); err != nil {
		t.Fatalf("Rollback(2): %v", err)
	}

	v, _ := migration.CurrentVersion(db)
	if v != 2 {
		t.Errorf("want schema v2 after rollback, got %d", v)
	}
	if tableExists(db, "test_table_b") {
		t.Error("test_table_b should be gone after rollback")
	}
	if !tableExists(db, "test_table_a") {
		t.Error("test_table_a should still exist")
	}
}

func TestRunner_IdempotentMigrate(t *testing.T) {
	db := openTempDB(t)
	r := migration.NewRunner("test", testMigrations())

	if err := r.Migrate(db); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	// Second call should be a no-op (no error, version unchanged).
	if err := r.Migrate(db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	v, _ := migration.CurrentVersion(db)
	if v != 3 {
		t.Errorf("want v3 still, got %d", v)
	}
}

// ---------------------------------------------------------------------------
// Transaction safety test
// ---------------------------------------------------------------------------

func TestRunner_TransactionRollbackOnError(t *testing.T) {
	db := openTempDB(t)

	migrations := []migration.Migration{
		{
			Version:     1,
			Description: "create table ok",
			Up: func(tx *sql.Tx) error {
				_, err := tx.Exec(`CREATE TABLE good_table (id INTEGER PRIMARY KEY)`)
				return err
			},
		},
		{
			Version:     2,
			Description: "migration that fails midway",
			Up: func(tx *sql.Tx) error {
				// First statement succeeds.
				if _, err := tx.Exec(`CREATE TABLE partial_table (id INTEGER PRIMARY KEY)`); err != nil {
					return err
				}
				// Second statement fails (intentional error).
				_, err := tx.Exec(`THIS IS NOT VALID SQL`)
				return err
			},
		},
	}

	r := migration.NewRunner("test", migrations)
	err := r.Migrate(db)
	if err == nil {
		t.Fatal("expected error from failing migration, got nil")
	}

	// Schema version must still be 1 (the last successful migration).
	v, _ := migration.CurrentVersion(db)
	if v != 1 {
		t.Errorf("want version 1 (last successful), got %d", v)
	}

	// partial_table must NOT exist (transaction was rolled back).
	if tableExists(db, "partial_table") {
		t.Error("partial_table should not exist — transaction should have been rolled back")
	}
}

// ---------------------------------------------------------------------------
// Backup tests
// ---------------------------------------------------------------------------

func TestBackupDatabase(t *testing.T) {
	_, dbPath := openTempFileDB(t)

	backupPath, err := migration.BackupDatabase(dbPath)
	if err != nil {
		t.Fatalf("BackupDatabase: %v", err)
	}
	t.Cleanup(func() { os.Remove(backupPath) })

	// Backup file must exist.
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Error("backup file does not exist")
	}

	// Original file must be untouched.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("original file should still exist")
	}
}

func TestBackupDatabase_IntegrityCheck(t *testing.T) {
	_, dbPath := openTempFileDB(t)

	backupPath, err := migration.BackupDatabase(dbPath)
	if err != nil {
		t.Fatalf("BackupDatabase: %v", err)
	}
	t.Cleanup(func() { os.Remove(backupPath) })

	// Manually verify the backup passes integrity_check.
	db, err := sql.Open("sqlite", backupPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer db.Close()

	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if result != "ok" {
		t.Errorf("integrity_check: want 'ok', got %q", result)
	}
}

func TestCleanOldBackups(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create a fake source file.
	f, err := os.Create(dbPath)
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	f.Close()

	// Create two backup files — one old, one recent.
	oldBackup := dbPath + ".bak.2020-01-01T00-00-00Z"
	recentBackup := dbPath + ".bak." + time.Now().UTC().Format("2006-01-02T15-04-05Z")

	for _, p := range []string{oldBackup, recentBackup} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatalf("write backup: %v", err)
		}
	}
	// Set mtime of old backup to 2 days ago.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldBackup, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if err := migration.CleanOldBackups(dbPath, 24*time.Hour); err != nil {
		t.Fatalf("CleanOldBackups: %v", err)
	}

	if _, err := os.Stat(oldBackup); !os.IsNotExist(err) {
		t.Error("old backup should have been removed")
	}
	if _, err := os.Stat(recentBackup); os.IsNotExist(err) {
		t.Error("recent backup should still exist")
	}
}

// ---------------------------------------------------------------------------
// Downgrade guard tests
// ---------------------------------------------------------------------------

func TestCheckVersion_OK(t *testing.T) {
	db := openTempDB(t)
	_ = migration.SetVersion(db, 3)

	if err := migration.CheckVersion(db, 3); err != nil {
		t.Errorf("CheckVersion equal versions: unexpected error: %v", err)
	}
	if err := migration.CheckVersion(db, 5); err != nil {
		t.Errorf("CheckVersion binary newer: unexpected error: %v", err)
	}
}

func TestCheckVersion_RejectsDowngrade(t *testing.T) {
	db := openTempDB(t)
	_ = migration.SetVersion(db, 5)

	err := migration.CheckVersion(db, 3)
	if err == nil {
		t.Fatal("expected error when schema(5) > binary(3), got nil")
	}
	// Check the error message contains expected text.
	want := "database schema version 5 is newer than binary version 3"
	if !containsString(err.Error(), want) {
		t.Errorf("error message %q does not contain %q", err.Error(), want)
	}
}

func TestCheckVersion_FreshDB(t *testing.T) {
	db := openTempDB(t)
	// No version set — CurrentVersion returns 0 — no error.
	if err := migration.CheckVersion(db, 1); err != nil {
		t.Errorf("CheckVersion on fresh DB: unexpected error: %v", err)
	}
}

func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
