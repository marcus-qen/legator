package sandbox

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ArtifactStore provides SQLite persistence for sandbox artifacts.
// It shares the same *sql.DB as the sandbox Store.
type ArtifactStore struct {
	db *sql.DB
}

// NewArtifactStore creates an ArtifactStore backed by the given database
// handle and ensures the sandbox_artifacts table and indexes exist.
func NewArtifactStore(db *sql.DB) (*ArtifactStore, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS sandbox_artifacts (
		id           TEXT    PRIMARY KEY,
		task_id      TEXT    NOT NULL DEFAULT '',
		sandbox_id   TEXT    NOT NULL,
		workspace_id TEXT    NOT NULL DEFAULT '',
		path         TEXT    NOT NULL DEFAULT '',
		kind         TEXT    NOT NULL DEFAULT 'file',
		size         INTEGER NOT NULL DEFAULT 0,
		sha256       TEXT    NOT NULL DEFAULT '',
		mime_type    TEXT    NOT NULL DEFAULT '',
		diff_summary TEXT    NOT NULL DEFAULT '',
		content      BLOB    NOT NULL DEFAULT '',
		created_at   TEXT    NOT NULL
	)`)
	if err != nil {
		return nil, fmt.Errorf("create sandbox_artifacts table: %w", err)
	}

	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_art_sandbox  ON sandbox_artifacts(sandbox_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_art_task     ON sandbox_artifacts(task_id)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_art_workspace ON sandbox_artifacts(workspace_id)`)

	return &ArtifactStore{db: db}, nil
}

// CreateArtifact persists an artifact (including its content).
// It assigns an ID if empty, computes SHA256 from Content if SHA256 is empty,
// and enforces the per-file and per-sandbox size limits.
func (as *ArtifactStore) CreateArtifact(a *Artifact) (*Artifact, error) {
	if a.ID == "" {
		a.ID = uuid.New().String()
	}
	a.CreatedAt = time.Now().UTC()

	if int64(len(a.Content)) > MaxArtifactSizeBytes {
		return nil, fmt.Errorf("artifact too large: %d bytes (limit %d)", len(a.Content), MaxArtifactSizeBytes)
	}

	// Enforce total sandbox quota.
	current, err := as.sandboxTotalSize(a.SandboxID, a.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("check sandbox artifact quota: %w", err)
	}
	if current+int64(len(a.Content)) > MaxSandboxArtifactBytes {
		return nil, fmt.Errorf("sandbox artifact quota exceeded: current %d + new %d > limit %d",
			current, len(a.Content), MaxSandboxArtifactBytes)
	}

	// Compute SHA256 server-side.
	sum := sha256.Sum256(a.Content)
	a.SHA256 = hex.EncodeToString(sum[:])
	a.Size = int64(len(a.Content))

	// Compute diff summary for diff kind.
	if a.Kind == ArtifactKindDiff && a.DiffSummary == "" {
		a.DiffSummary = ParseDiffSummary(a.Content)
	}

	_, err = as.db.Exec(`INSERT INTO sandbox_artifacts
		(id, task_id, sandbox_id, workspace_id, path, kind, size, sha256,
		 mime_type, diff_summary, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID,
		a.TaskID,
		a.SandboxID,
		a.WorkspaceID,
		a.Path,
		a.Kind,
		a.Size,
		a.SHA256,
		a.MimeType,
		a.DiffSummary,
		a.Content,
		a.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("insert sandbox artifact: %w", err)
	}
	return a, nil
}

// GetArtifact fetches a single artifact (metadata + content) by ID.
// Returns (nil, nil) when not found or workspace is mismatched.
func (as *ArtifactStore) GetArtifact(id, workspaceID string) (*Artifact, error) {
	row := as.db.QueryRow(`SELECT id, task_id, sandbox_id, workspace_id, path, kind,
		size, sha256, mime_type, diff_summary, content, created_at
		FROM sandbox_artifacts WHERE id = ?`, id)
	a, err := scanArtifact(row, true)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if workspaceID != "" && a.WorkspaceID != "" && a.WorkspaceID != workspaceID {
		return nil, nil
	}
	return a, nil
}

// ListArtifacts returns artifact metadata (no content blob) matching the
// filter, ordered by created_at ASC.
func (as *ArtifactStore) ListArtifacts(f ArtifactListFilter) ([]*Artifact, error) {
	query := `SELECT id, task_id, sandbox_id, workspace_id, path, kind,
		size, sha256, mime_type, diff_summary, '' AS content, created_at
		FROM sandbox_artifacts WHERE 1=1`
	var args []any

	if f.SandboxID != "" {
		query += " AND sandbox_id = ?"
		args = append(args, f.SandboxID)
	}
	if f.WorkspaceID != "" {
		query += " AND workspace_id = ?"
		args = append(args, f.WorkspaceID)
	}
	if f.TaskID != "" {
		query += " AND task_id = ?"
		args = append(args, f.TaskID)
	}

	query += " ORDER BY created_at ASC"

	rows, err := as.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sandbox artifacts: %w", err)
	}
	defer rows.Close()

	var artifacts []*Artifact
	for rows.Next() {
		a, err := scanArtifact(rows, false)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, a)
	}
	return artifacts, rows.Err()
}

// DeleteArtifacts removes all artifacts for a sandbox. Used during destroy.
func (as *ArtifactStore) DeleteArtifacts(sandboxID string) error {
	_, err := as.db.Exec(`DELETE FROM sandbox_artifacts WHERE sandbox_id = ?`, sandboxID)
	if err != nil {
		return fmt.Errorf("delete sandbox artifacts: %w", err)
	}
	return nil
}

// sandboxTotalSize returns the sum of stored artifact sizes for a sandbox.
func (as *ArtifactStore) sandboxTotalSize(sandboxID, workspaceID string) (int64, error) {
	query := `SELECT COALESCE(SUM(size), 0) FROM sandbox_artifacts WHERE sandbox_id = ?`
	args := []any{sandboxID}
	if workspaceID != "" {
		query += " AND workspace_id = ?"
		args = append(args, workspaceID)
	}
	var total int64
	err := as.db.QueryRow(query, args...).Scan(&total)
	return total, err
}

// ── internal helpers ─────────────────────────────────────────────────────────

type artifactRowScanner interface {
	Scan(dest ...any) error
}

func scanArtifact(scanner artifactRowScanner, withContent bool) (*Artifact, error) {
	var (
		a         Artifact
		content   []byte
		createdAt string
	)

	err := scanner.Scan(
		&a.ID,
		&a.TaskID,
		&a.SandboxID,
		&a.WorkspaceID,
		&a.Path,
		&a.Kind,
		&a.Size,
		&a.SHA256,
		&a.MimeType,
		&a.DiffSummary,
		&content,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}

	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if withContent {
		a.Content = content
	}
	return &a, nil
}
