package sandbox

import (
	"bytes"
	"fmt"
	"time"
)

// Artifact kind constants.
const (
	ArtifactKindFile = "file"
	ArtifactKindDiff = "diff"
	ArtifactKindLog  = "log"
)

// Size limits.
const (
	MaxArtifactSizeBytes    int64 = 5 * 1024 * 1024  // 5 MB per artifact
	MaxSandboxArtifactBytes int64 = 50 * 1024 * 1024 // 50 MB total per sandbox
)

// Artifact is the domain model for an artifact produced by a sandbox task.
type Artifact struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id"`
	SandboxID   string    `json:"sandbox_id"`
	WorkspaceID string    `json:"workspace_id"`
	Path        string    `json:"path"`         // file path relative to workspace root
	Kind        string    `json:"kind"`         // "file", "diff", "log"
	Size        int64     `json:"size"`         // bytes
	SHA256      string    `json:"sha256"`       // content hash
	MimeType    string    `json:"mime_type"`    // detected or provided
	DiffSummary string    `json:"diff_summary"` // for kind=diff: "+3 -1" style summary
	Content     []byte    `json:"-"`            // actual content (not serialised to JSON)
	CreatedAt   time.Time `json:"created_at"`
}

// ArtifactListFilter controls which artifacts are returned by ArtifactStore.ListArtifacts.
type ArtifactListFilter struct {
	SandboxID   string
	TaskID      string
	WorkspaceID string
}

// ParseDiffSummary reads a unified diff payload and returns an "+N -M" style
// summary string (e.g. "+3 -1").  Lines starting with "+++" / "---" (file
// headers) are excluded from the counts.
func ParseDiffSummary(content []byte) string {
	added, deleted := 0, 0
	for _, line := range bytes.Split(content, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case '+':
			if bytes.HasPrefix(line, []byte("+++")) {
				continue
			}
			added++
		case '-':
			if bytes.HasPrefix(line, []byte("---")) {
				continue
			}
			deleted++
		}
	}
	return fmt.Sprintf("+%d -%d", added, deleted)
}
