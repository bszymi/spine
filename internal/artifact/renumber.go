package artifact

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
)

// RenumberResult contains the result of a successful renumber operation.
type RenumberResult struct {
	OldID   string // e.g., TASK-006
	NewID   string // e.g., TASK-007
	OldPath string // artifact-relative path before rename
	NewPath string // artifact-relative path after rename
}

// IsIDCollision checks if a merge conflict is caused by an artifact ID collision.
// It examines the conflicting files to determine if the conflict is a path collision
// where the planned artifact's path already exists on the target branch.
func IsIDCollision(ctx context.Context, gitClient git.GitClient, branchRef, targetRef, artifactPath string) (bool, error) {
	// Check if the artifact path exists on the target branch.
	_, err := gitClient.ReadFile(ctx, targetRef, artifactPath)
	if err != nil {
		// File doesn't exist on target — not an ID collision.
		return false, nil
	}
	// File exists on both branches — this is an ID collision.
	return true, nil
}

// RenumberArtifact renames an artifact on the filesystem to a new ID.
// It updates the file/directory path, the front-matter ID field, and the
// markdown heading. Returns the old and new paths.
//
// The caller must be on the correct branch and commit the changes after.
func RenumberArtifact(repoRoot, artifactPath string, artifactType domain.ArtifactType, oldID, newID string) (*RenumberResult, error) {
	fullOldPath := filepath.Join(repoRoot, artifactPath)

	// Read the current content.
	content, err := os.ReadFile(fullOldPath)
	if err != nil {
		return nil, fmt.Errorf("read artifact %s: %w", artifactPath, err)
	}

	// Replace the ID in front-matter and heading.
	updated := replaceArtifactID(string(content), oldID, newID)

	// Compute the new path.
	newPath := renamePathForNewID(artifactPath, oldID, newID)
	fullNewPath := filepath.Join(repoRoot, newPath)

	// Create parent directory if needed.
	if err := os.MkdirAll(filepath.Dir(fullNewPath), 0o755); err != nil {
		return nil, fmt.Errorf("create directory for %s: %w", newPath, err)
	}

	// Write to the new path.
	if err := os.WriteFile(fullNewPath, []byte(updated), 0o644); err != nil {
		return nil, fmt.Errorf("write artifact %s: %w", newPath, err)
	}

	// Remove the old file if the path changed.
	if artifactPath != newPath {
		if err := os.Remove(fullOldPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove old artifact %s: %w", artifactPath, err)
		}
		// Clean up empty parent directories left behind.
		cleanEmptyParents(filepath.Dir(fullOldPath), repoRoot)
	}

	return &RenumberResult{
		OldID:   oldID,
		NewID:   newID,
		OldPath: artifactPath,
		NewPath: newPath,
	}, nil
}

// UpdateLinksToRenamedArtifact scans all markdown files in a directory tree
// and updates any links that reference the old path to point to the new path.
func UpdateLinksToRenamedArtifact(repoRoot, searchDir, oldPath, newPath string) error {
	fullSearchDir := filepath.Join(repoRoot, searchDir)

	return filepath.Walk(fullSearchDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files
		}

		// Replace references to the old path with the new path.
		// Links use absolute paths with leading / (e.g., /initiatives/...)
		oldRef := "/" + oldPath
		newRef := "/" + newPath
		updated := strings.ReplaceAll(string(content), oldRef, newRef)

		if updated != string(content) {
			return os.WriteFile(path, []byte(updated), 0o644)
		}
		return nil
	})
}

// replaceArtifactID replaces the ID in front-matter and markdown heading.
func replaceArtifactID(content, oldID, newID string) string {
	// Replace front-matter id field: "id: TASK-006" → "id: TASK-007"
	idPattern := regexp.MustCompile(`(?m)^id:\s*` + regexp.QuoteMeta(oldID) + `\s*$`)
	content = idPattern.ReplaceAllString(content, "id: "+newID)

	// Replace heading: "# TASK-006" → "# TASK-007"
	content = strings.ReplaceAll(content, "# "+oldID, "# "+newID)

	return content
}

// renamePathForNewID computes the new file path after an ID change.
// Handles both file-based artifacts (TASK-006-slug.md → TASK-007-slug.md)
// and directory-based artifacts (EPIC-006-slug/epic.md → EPIC-007-slug/epic.md).
func renamePathForNewID(artifactPath, oldID, newID string) string {
	return strings.Replace(artifactPath, strings.ToLower(oldID), strings.ToLower(newID), 1)
}

// cleanEmptyParents removes empty parent directories up to (but not including) stopAt.
func cleanEmptyParents(dir, stopAt string) {
	for dir != stopAt && dir != "." && dir != "/" {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		os.Remove(dir)
		dir = filepath.Dir(dir)
	}
}
