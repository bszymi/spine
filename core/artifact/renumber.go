package artifact

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/worktreefs"
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
		// A blob refused for its size exists (INIT-023 TASK-003): that
		// is a collision, not a free path. So is a gitlink, a directory or
		// a file entry recording a tag, a tree or a commit, which the git
		// client refuses to read as a file (git.ErrNotBlob, wave 4
		// follow-up and its review): the path is taken (a directory read
		// as its listing, and so as a collision, before).
		if errors.Is(err, git.ErrBlobTooLarge) || errors.Is(err, git.ErrNotBlob) {
			return true, nil
		}
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
//
// All file I/O goes through worktreefs (INIT-023 TASK-004): artifactPath
// and the derived new path must be clean repo-relative paths, and a
// symbolic link anywhere in either is refused rather than followed. The
// function has no production caller today; it is guarded so the sink does
// not come back when one is wired.
func RenumberArtifact(repoRoot, artifactPath string, artifactType domain.ArtifactType, oldID, newID string) (*RenumberResult, error) {
	tree, err := worktreefs.Open(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	defer func() { _ = tree.Close() }()

	oldRel, err := worktreefs.Clean(artifactPath)
	if err != nil {
		return nil, fmt.Errorf("artifact path: %w", err)
	}

	// Read the current content.
	content, err := tree.ReadFile(oldRel)
	if err != nil {
		return nil, fmt.Errorf("read artifact %s: %w", artifactPath, err)
	}

	// Replace the ID in front-matter and heading.
	updated := replaceArtifactID(string(content), oldID, newID)

	// Compute the new path.
	newPath := renamePathForNewID(artifactPath, oldID, newID)
	newRel, err := worktreefs.Clean(newPath)
	if err != nil {
		return nil, fmt.Errorf("new artifact path: %w", err)
	}

	// Write to the new path (creating parent directories as needed).
	if err := tree.WriteFile(newRel, []byte(updated), 0o644); err != nil {
		return nil, fmt.Errorf("write artifact %s: %w", newPath, err)
	}

	// Remove the old file if the path changed.
	if oldRel != newRel {
		if err := tree.Remove(oldRel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("remove old artifact %s: %w", artifactPath, err)
		}
		// Clean up empty parent directories left behind.
		cleanEmptyParents(tree, path.Dir(oldRel))
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
//
// searchDir is repo-relative ("", "." and "/" mean the repository root).
// The scan goes through worktreefs.WalkRegularFiles: it never enters or
// rewrites anything under .git, and a symbolic link (file or directory) is
// skipped, never read or written through. Unreadable or oversized files are
// skipped, as before.
func UpdateLinksToRenamedArtifact(repoRoot, searchDir, oldPath, newPath string) error {
	tree, err := worktreefs.Open(repoRoot)
	if err != nil {
		return fmt.Errorf("open repository: %w", err)
	}
	defer func() { _ = tree.Close() }()

	dir := strings.Trim(filepath.ToSlash(searchDir), "/")
	if dir == "." {
		dir = ""
	}

	// Links use absolute paths with leading / (e.g., /initiatives/...)
	oldRef := "/" + oldPath
	newRef := "/" + newPath
	return tree.WalkRegularFiles(dir, func(rel string) error {
		if !strings.HasSuffix(rel, ".md") {
			return nil
		}
		content, err := tree.ReadFile(rel)
		if err != nil {
			return nil // skip unreadable files
		}
		updated := strings.ReplaceAll(string(content), oldRef, newRef)
		if updated != string(content) {
			return tree.Replace(rel, []byte(updated))
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

// cleanEmptyParents removes empty parent directories of a repo-relative
// directory, deepest first, up to (but not including) the repository root.
// It stops at the first directory that is not empty (or cannot be
// removed); removal goes through the tree, so a symlinked directory is
// never followed.
func cleanEmptyParents(tree *worktreefs.Tree, dir string) {
	for dir != "." && dir != "" && dir != "/" {
		if err := tree.RemoveEmptyDir(dir); err != nil {
			return
		}
		dir = path.Dir(dir)
	}
}
