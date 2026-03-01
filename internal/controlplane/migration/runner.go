package migration

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
)

// Migration describes a single schema change.
type Migration struct {
	// Version is the schema version this migration produces.
	Version int
	// Description is a human-readable summary.
	Description string
	// Up applies the migration inside tx.
	Up func(tx *sql.Tx) error
	// Down reverts the migration inside tx.
	Down func(tx *sql.Tx) error
}

// Runner applies ordered migrations to a database.
type Runner struct {
	storeName  string
	migrations []Migration
}

// NewRunner creates a Runner for storeName with the given migrations.
// Migrations are sorted by Version ascending automatically.
func NewRunner(storeName string, migrations []Migration) *Runner {
	sorted := make([]Migration, len(migrations))
	copy(sorted, migrations)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Version < sorted[j].Version
	})
	return &Runner{storeName: storeName, migrations: sorted}
}

// Migrate applies all pending up-migrations in version order.
// Each migration runs in its own transaction; on error the transaction is
// rolled back and the error is returned immediately.
func (r *Runner) Migrate(db *sql.DB) error {
	current, err := CurrentVersion(db)
	if err != nil {
		return fmt.Errorf("runner[%s] read current version: %w", r.storeName, err)
	}

	for _, m := range r.migrations {
		if m.Version <= current {
			continue
		}
		if err := r.applyUp(db, m); err != nil {
			return err
		}
	}
	return nil
}

// MigrateTo applies up-migrations up to and including targetVersion.
func (r *Runner) MigrateTo(db *sql.DB, targetVersion int) error {
	current, err := CurrentVersion(db)
	if err != nil {
		return fmt.Errorf("runner[%s] read current version: %w", r.storeName, err)
	}

	for _, m := range r.migrations {
		if m.Version <= current || m.Version > targetVersion {
			continue
		}
		if err := r.applyUp(db, m); err != nil {
			return err
		}
	}
	return nil
}

// Rollback applies down-migrations until the schema reaches targetVersion.
// Migrations are applied in reverse order.
func (r *Runner) Rollback(db *sql.DB, targetVersion int) error {
	current, err := CurrentVersion(db)
	if err != nil {
		return fmt.Errorf("runner[%s] read current version: %w", r.storeName, err)
	}

	// Iterate in reverse.
	for i := len(r.migrations) - 1; i >= 0; i-- {
		m := r.migrations[i]
		if m.Version <= targetVersion || m.Version > current {
			continue
		}
		if err := r.applyDown(db, m, targetVersion); err != nil {
			return err
		}
	}
	return nil
}

// applyUp runs m.Up inside a transaction and updates the schema version.
func (r *Runner) applyUp(db *sql.DB, m Migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("runner[%s] begin tx for v%d: %w", r.storeName, m.Version, err)
	}

	if err := m.Up(tx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("runner[%s] up v%d (%s): %w", r.storeName, m.Version, m.Description, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("runner[%s] commit v%d: %w", r.storeName, m.Version, err)
	}

	// Update schema version after successful commit.
	if err := SetVersion(db, m.Version); err != nil {
		return fmt.Errorf("runner[%s] set version %d: %w", r.storeName, m.Version, err)
	}

	log.Printf("migration[%s]: applied v%d — %s", r.storeName, m.Version, m.Description)
	return nil
}

// applyDown runs m.Down inside a transaction and resets the schema version to
// the previous migration's version (or targetVersion when no prior migration
// exists).
func (r *Runner) applyDown(db *sql.DB, m Migration, targetVersion int) error {
	if m.Down == nil {
		return fmt.Errorf("runner[%s] no Down function for v%d", r.storeName, m.Version)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("runner[%s] begin tx for rollback v%d: %w", r.storeName, m.Version, err)
	}

	if err := m.Down(tx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("runner[%s] down v%d (%s): %w", r.storeName, m.Version, m.Description, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("runner[%s] commit rollback v%d: %w", r.storeName, m.Version, err)
	}

	// After rolling back m, the schema is at the previous version.
	// Find the highest version that is still <= targetVersion.
	prevVersion := targetVersion
	for _, other := range r.migrations {
		if other.Version < m.Version && other.Version > prevVersion {
			prevVersion = other.Version
		}
	}

	if err := SetVersion(db, prevVersion); err != nil {
		return fmt.Errorf("runner[%s] reset version to %d: %w", r.storeName, prevVersion, err)
	}

	log.Printf("migration[%s]: rolled back v%d — %s (schema now at v%d)",
		r.storeName, m.Version, m.Description, prevVersion)
	return nil
}
