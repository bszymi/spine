package artifact

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
)

// DiscoveryResult holds the results of a repository scan.
type DiscoveryResult struct {
	Artifacts []*domain.Artifact // parsed artifacts
	Workflows []string           // paths to workflow YAML files
	Skipped   []string           // paths skipped (non-artifact .md files)
	Errors    []DiscoveryError   // files that failed to parse
	// Refused lists the candidates discovery declined to project — an
	// oversized .md, or an artifact or workflow candidate at an
	// UnsafePath (INIT-023 TASK-003), or a .md that is a gitlink, not a
	// file, or a file entry recording a tag, a tree or a commit (wave 4
	// follow-up and its review). They are not Errors: a refused file never
	// makes a rebuild end in error.
	Refused []RefusedFile
}

// DiscoveryError records a file that was discovered but failed to parse
// or to read.
type DiscoveryError struct {
	Path    string
	Message string
	// Ref is the git ref the failed read was attempted at. Set on
	// ChangeSet.ReadErrors; empty for parse errors.
	Ref string
	// Err is the underlying read error, kept (not only its text) so a
	// caller can classify it (errors.Is / errors.As) together with Ref
	// and Path. Set on ChangeSet.ReadErrors; nil for parse errors.
	Err error
}

// ChangeSet represents artifacts affected by a Git diff.
type ChangeSet struct {
	Created  []*domain.Artifact
	Modified []*domain.Artifact
	Deleted  []string // paths only (content no longer available)
	// ReadErrors records every ReadFile failure discovery skipped past.
	// The skip itself is unchanged (a path whose blob cannot be read is
	// left out of Created/Modified/Deleted, as it always was), but the
	// failure is no longer silent: the projection's IncrementalSync
	// holds the sync on every one it cannot prove deterministic (from the
	// commit's tree, not from git's stderr) instead of advancing past a
	// change it never applied (INIT-023 TASK-011, audit P3-14 class).
	// Callers that only need the change lists — branch validation — can
	// ignore it.
	ReadErrors []DiscoveryError
	// Refused lists the changed files discovery declined to project
	// (INIT-023 TASK-003): an added, modified or renamed-to .md that is
	// oversized or at an UnsafePath, or that is a gitlink rather than a
	// file, or a file entry recording a tag, a tree or a commit
	// (git.ErrNotBlob, wave 4 follow-up and its review). A refused file
	// is never in Created or Modified; a governed row it replaces is in
	// Deleted. Refusals are deterministic for the commit, so they are not
	// ReadErrors and never hold a sync.
	Refused []RefusedFile
}

func (c *ChangeSet) recordReadError(path, ref string, err error) {
	c.ReadErrors = append(c.ReadErrors, DiscoveryError{
		Path:    path,
		Message: err.Error(),
		Ref:     ref,
		Err:     err,
	})
}

// DiscoverAll performs a full repository scan at the given ref.
// Returns all artifacts, workflow definitions, and skipped files.
// The artifactsDir parameter scopes discovery to a subdirectory
// (e.g., "spine"). Use "/" or "" for repo root (default behavior).
func DiscoverAll(ctx context.Context, gitClient git.GitClient, ref string, artifactsDir ...string) (*DiscoveryResult, error) {
	if ref == "" {
		ref = "HEAD"
	}

	dir := "/"
	if len(artifactsDir) > 0 && artifactsDir[0] != "" {
		dir = artifactsDir[0]
	}

	prefix := ""
	if dir != "/" {
		prefix = dir + "/"
	}

	// Only the candidates the loop below can use are listed (INIT-024
	// TASK-004, review finding N-6): with a git client that filters while
	// it lists (git.PathFilterLister), a tree with a huge listing and few
	// artifacts no longer exceeds the listing limit. The loop applies the
	// same checks again, so the result is the one an unfiltered listing
	// gives.
	files, err := git.ListFilesKeeping(ctx, gitClient, ref, func(file string) bool {
		return discoveryCandidate(file, prefix)
	})
	if err != nil {
		return nil, err
	}

	result := &DiscoveryResult{}

	for _, file := range files {
		// Filter to artifacts directory if configured.
		if prefix != "" && !strings.HasPrefix(file, prefix) {
			continue
		}

		// Strip prefix for artifact-relative paths.
		artifactPath := file
		if prefix != "" {
			artifactPath = strings.TrimPrefix(file, prefix)
		}

		// Workflow definitions. An unsafe path is refused, never listed:
		// the projection would otherwise pass it to the store.
		if IsWorkflowPath(artifactPath) {
			if UnsafePath(file) {
				result.Refused = append(result.Refused, unsafeRefusal(file))
				continue
			}
			result.Workflows = append(result.Workflows, file)
			continue
		}

		// Only process .md files
		if !strings.HasSuffix(file, ".md") {
			continue
		}

		// Shape-reference files under templates/ are not governed
		// artifacts even when their frontmatter declares a valid
		// type — they exist as copy-paste sources for new artifacts.
		// See IsArtifact / IsWorkflowPath for the other path rules.
		if IsTemplatePath(artifactPath) {
			result.Skipped = append(result.Skipped, artifactPath)
			continue
		}

		// An unsafe path is refused without reading it (INIT-023
		// TASK-003): it can never be stored, so its content is moot.
		if UnsafePath(file) {
			result.Refused = append(result.Refused, unsafeRefusal(file))
			continue
		}

		content, refused, err := readForDiscovery(ctx, gitClient, ref, file)
		if err != nil {
			result.Errors = append(result.Errors, DiscoveryError{
				Path:    artifactPath,
				Message: err.Error(),
			})
			continue
		}
		if refused != nil {
			// Oversized: skip, record and advance (owner question 1).
			// It cannot become readable at this commit, so failing the
			// rebuild would buy nothing and let one push freeze it.
			result.Refused = append(result.Refused, *refused)
			continue
		}

		if !IsArtifact(content) {
			result.Skipped = append(result.Skipped, artifactPath)
			continue
		}

		a, err := Parse(artifactPath, content)
		if err != nil {
			result.Errors = append(result.Errors, DiscoveryError{
				Path:    artifactPath,
				Message: err.Error(),
			})
			continue
		}

		result.Artifacts = append(result.Artifacts, a)
	}

	return result, nil
}

// discoveryCandidate reports whether DiscoverAll's loop does anything
// with file: a workflow definition or a .md file inside the artifacts
// directory (prefix, "" for the repository root). Everything else is
// skipped without a trace, so it need not be listed.
func discoveryCandidate(file, prefix string) bool {
	if prefix != "" && !strings.HasPrefix(file, prefix) {
		return false
	}
	return IsWorkflowPath(strings.TrimPrefix(file, prefix)) || strings.HasSuffix(file, ".md")
}

// DiscoverChanges detects artifacts affected by changes between two Git refs.
// Per Git Integration §8.2: incremental change discovery from commit diffs.
func DiscoverChanges(ctx context.Context, gitClient git.GitClient, fromRef, toRef string) (*ChangeSet, error) {
	diffs, err := gitClient.Diff(ctx, fromRef, toRef)
	if err != nil {
		return nil, err
	}
	return DiscoverChangesFromDiffs(ctx, gitClient, fromRef, toRef, diffs)
}

// DiscoverChangesFromDiffs is DiscoverChanges over a diff the caller has
// already computed. The projection computes one Diff per sync and feeds
// the same list to artifact discovery and to its governance-file loop,
// so the two can never see different windows and a failed second Diff
// can no longer be dropped (INIT-023 TASK-011, audit P3-14).
//
// Every ReadFile failure that discovery skips past is recorded in
// ChangeSet.ReadErrors. The returned error is reserved for failures that
// make the whole changeset unusable; it is nil today.
//
// Refusals (INIT-023 TASK-003). A path that is UnsafePath is never read
// and never reaches Created or Modified. It reaches Deleted only when it
// is a StoredUnsafePath (valid UTF-8): a projection built with
// core.quotePath=false before TASK-003 may hold its row, which must not
// outlive the file (wave 3 integration). Invalid UTF-8 never reaches the
// store. A read the git client refuses for size (git.ErrBlobTooLarge) is
// not a ReadError, and neither is one it refuses because the path names a
// gitlink, a directory or a file entry recording a tag, a tree or a commit
// (git.ErrNotBlob, RefusedNotAFile; INIT-023 wave 4 follow-up and its
// review) — "oversized" below covers both:
//
//	added     unsafe or oversized new path        → Refused
//	deleted   old content oversized               → Deleted (idempotent: no stale row survives)
//	          unsafe path                         → Deleted when valid UTF-8 (unread), else skipped
//	renamed   old side oversized                  → Deleted
//	          old side unsafe                     → Deleted when valid UTF-8 (unread)
//	          new side unsafe or oversized        → Refused
//	modified  unsafe path                         → Refused, and Deleted when valid UTF-8
//	          new content oversized               → Refused, and Deleted when the old
//	                                                side was an artifact — the governed
//	                                                row is removed, as for a parse failure
//	          old content oversized               → treated as "was an artifact", so a
//	                                                now-non-artifact file is Deleted
func DiscoverChangesFromDiffs(ctx context.Context, gitClient git.GitClient, fromRef, toRef string, diffs []git.FileDiff) (*ChangeSet, error) {
	changeset := &ChangeSet{}

	for _, diff := range diffs {
		isMdNew := strings.HasSuffix(diff.Path, ".md")
		isMdOld := diff.OldPath != "" && strings.HasSuffix(diff.OldPath, ".md")

		// Skip if neither old nor new path is a .md file
		if !isMdNew && !isMdOld {
			continue
		}

		switch diff.Status {
		case "deleted":
			if UnsafePath(diff.Path) {
				if StoredUnsafePath(diff.Path) {
					changeset.Deleted = append(changeset.Deleted, diff.Path)
				}
				continue
			}
			// Only include if the deleted file was actually an artifact at fromRef
			oldContent, oldRefused, err := readForDiscovery(ctx, gitClient, fromRef, diff.Path)
			if err != nil {
				changeset.recordReadError(diff.Path, fromRef, err)
				continue
			}
			if oldRefused != nil || IsArtifact(oldContent) {
				changeset.Deleted = append(changeset.Deleted, diff.Path)
			}

		case "added":
			if UnsafePath(diff.Path) {
				changeset.Refused = append(changeset.Refused, unsafeRefusal(diff.Path))
				continue
			}
			content, refused, err := readForDiscovery(ctx, gitClient, toRef, diff.Path)
			if err != nil {
				changeset.recordReadError(diff.Path, toRef, err)
				continue
			}
			if refused != nil {
				changeset.Refused = append(changeset.Refused, *refused)
				continue
			}
			if !IsArtifact(content) {
				continue
			}
			a, err := Parse(diff.Path, content)
			if err != nil {
				continue
			}
			changeset.Created = append(changeset.Created, a)

		case "renamed":
			// Renames are modeled as delete old path + create new path
			// This ensures path-keyed projections are updated correctly

			// Delete old path if it was an artifact (an unsafe one is
			// never read: removed when the store can hold it).
			if diff.OldPath != "" && isMdOld && UnsafePath(diff.OldPath) {
				if StoredUnsafePath(diff.OldPath) {
					changeset.Deleted = append(changeset.Deleted, diff.OldPath)
				}
			} else if diff.OldPath != "" && isMdOld {
				oldContent, oldRefused, err := readForDiscovery(ctx, gitClient, fromRef, diff.OldPath)
				if err != nil {
					changeset.recordReadError(diff.OldPath, fromRef, err)
				} else if oldRefused != nil || IsArtifact(oldContent) {
					changeset.Deleted = append(changeset.Deleted, diff.OldPath)
				}
			}

			// Create new path if it's a .md artifact
			if !isMdNew {
				continue
			}
			if UnsafePath(diff.Path) {
				changeset.Refused = append(changeset.Refused, unsafeRefusal(diff.Path))
				continue
			}
			content, refused, err := readForDiscovery(ctx, gitClient, toRef, diff.Path)
			if err != nil {
				changeset.recordReadError(diff.Path, toRef, err)
				continue
			}
			if refused != nil {
				changeset.Refused = append(changeset.Refused, *refused)
				continue
			}
			if !IsArtifact(content) {
				continue
			}
			a, err := Parse(diff.Path, content)
			if err != nil {
				continue
			}
			changeset.Created = append(changeset.Created, a)

		case "modified":
			if !isMdNew {
				continue
			}
			if UnsafePath(diff.Path) {
				changeset.Refused = append(changeset.Refused, unsafeRefusal(diff.Path))
				if StoredUnsafePath(diff.Path) {
					changeset.Deleted = append(changeset.Deleted, diff.Path)
				}
				continue
			}

			// Check if file was an artifact at fromRef. An oversized old
			// blob counts as "was an artifact": it may have been
			// projected before the read limit existed, and deleting a
			// row that is not there is harmless.
			wasArtifact := false
			oldContent, oldRefused, oldErr := readForDiscovery(ctx, gitClient, fromRef, diff.Path)
			switch {
			case oldErr != nil:
				// Unreadable old blob is treated as "was not an
				// artifact" (unchanged); record it so a transient
				// failure cannot silently turn a delete into a skip.
				changeset.recordReadError(diff.Path, fromRef, oldErr)
			case oldRefused != nil || IsArtifact(oldContent):
				wasArtifact = true
			}

			content, refused, err := readForDiscovery(ctx, gitClient, toRef, diff.Path)
			if err != nil {
				changeset.recordReadError(diff.Path, toRef, err)
				continue
			}
			if refused != nil {
				// Modified past the read limit: the governed row is
				// removed (as for a parse failure below) rather than
				// left serving content that is no longer on main.
				changeset.Refused = append(changeset.Refused, *refused)
				if wasArtifact {
					changeset.Deleted = append(changeset.Deleted, diff.Path)
				}
				continue
			}

			isArtifactNow := IsArtifact(content)

			// Determine the correct change type
			switch {
			case !wasArtifact && isArtifactNow:
				// Non-artifact became an artifact → Created
				a, err := Parse(diff.Path, content)
				if err != nil {
					continue
				}
				changeset.Created = append(changeset.Created, a)

			case wasArtifact && !isArtifactNow:
				// Artifact became non-artifact → Deleted
				changeset.Deleted = append(changeset.Deleted, diff.Path)

			case wasArtifact && isArtifactNow:
				// Artifact modified → Modified (or Deleted if parse fails)
				a, err := Parse(diff.Path, content)
				if err != nil {
					changeset.Deleted = append(changeset.Deleted, diff.Path)
					continue
				}
				changeset.Modified = append(changeset.Modified, a)

			default:
				// Was not artifact, still not artifact → skip
				continue
			}
		}
	}

	return changeset, nil
}

// ClassifyByType groups artifacts by their type.
func ClassifyByType(artifacts []*domain.Artifact) map[domain.ArtifactType][]*domain.Artifact {
	result := make(map[domain.ArtifactType][]*domain.Artifact)
	for _, a := range artifacts {
		result[a.Type] = append(result[a.Type], a)
	}
	return result
}

// IsWorkflowPath returns true if the path is a workflow definition YAML file.
// Exported for use by Projection Service.
func IsWorkflowPath(path string) bool {
	return strings.HasPrefix(path, "workflows/") &&
		(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml"))
}

// IsTemplatePath returns true if the path is under the top-level
// templates/ directory. These files hold frontmatter shape examples
// (e.g. templates/task-template.md with type: Task) and are excluded
// from artifact discovery even though IsArtifact would otherwise
// admit them.
func IsTemplatePath(path string) bool {
	return strings.HasPrefix(path, "templates/")
}

// DiscoverWorkflows returns all workflow definition paths at a given ref.
// Paths are returned as repo-relative for git operations.
func DiscoverWorkflows(ctx context.Context, gitClient git.GitClient, ref string, artifactsDir ...string) ([]string, error) {
	if ref == "" {
		ref = "HEAD"
	}

	dir := "/"
	if len(artifactsDir) > 0 && artifactsDir[0] != "" {
		dir = artifactsDir[0]
	}

	prefix := ""
	if dir != "/" {
		prefix = dir + "/"
	}

	// Listed through the same filter as DiscoverAll's (INIT-024 TASK-004):
	// only workflow definitions inside the directory are kept.
	files, err := git.ListFilesKeeping(ctx, gitClient, ref, func(file string) bool {
		if prefix != "" && !strings.HasPrefix(file, prefix) {
			return false
		}
		return IsWorkflowPath(strings.TrimPrefix(file, prefix))
	})
	if err != nil {
		return nil, err
	}

	var workflows []string
	for _, file := range files {
		// Filter by artifacts directory.
		artifactPath := file
		if prefix != "" {
			if !strings.HasPrefix(file, prefix) {
				continue
			}
			artifactPath = strings.TrimPrefix(file, prefix)
		}
		if IsWorkflowPath(artifactPath) {
			workflows = append(workflows, file)
		}
	}
	return workflows, nil
}

// FilterByExtension filters file paths by extension.
func FilterByExtension(files []string, ext string) []string {
	var result []string
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	for _, f := range files {
		if filepath.Ext(f) == ext {
			result = append(result, f)
		}
	}
	return result
}
