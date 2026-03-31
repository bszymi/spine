package artifact

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
)

// DiscoveryResult holds the results of a repository scan.
type DiscoveryResult struct {
	Artifacts []*domain.Artifact // parsed artifacts
	Workflows []string           // paths to workflow YAML files
	Skipped   []string           // paths skipped (non-artifact .md files)
	Errors    []DiscoveryError   // files that failed to parse
}

// DiscoveryError records a file that was discovered but failed to parse.
type DiscoveryError struct {
	Path    string
	Message string
}

// ChangeSet represents artifacts affected by a Git diff.
type ChangeSet struct {
	Created  []*domain.Artifact
	Modified []*domain.Artifact
	Deleted  []string // paths only (content no longer available)
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

	files, err := gitClient.ListFiles(ctx, ref, "")
	if err != nil {
		return nil, err
	}

	prefix := ""
	if dir != "/" {
		prefix = dir + "/"
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

		// Workflow definitions
		if IsWorkflowPath(artifactPath) {
			result.Workflows = append(result.Workflows, file)
			continue
		}

		// Only process .md files
		if !strings.HasSuffix(file, ".md") {
			continue
		}

		content, err := gitClient.ReadFile(ctx, ref, file)
		if err != nil {
			result.Errors = append(result.Errors, DiscoveryError{
				Path:    artifactPath,
				Message: err.Error(),
			})
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

// DiscoverChanges detects artifacts affected by changes between two Git refs.
// Per Git Integration §8.2: incremental change discovery from commit diffs.
func DiscoverChanges(ctx context.Context, gitClient git.GitClient, fromRef, toRef string) (*ChangeSet, error) {
	diffs, err := gitClient.Diff(ctx, fromRef, toRef)
	if err != nil {
		return nil, err
	}

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
			// Only include if the deleted file was actually an artifact at fromRef
			oldContent, err := gitClient.ReadFile(ctx, fromRef, diff.Path)
			if err != nil {
				continue
			}
			if IsArtifact(oldContent) {
				changeset.Deleted = append(changeset.Deleted, diff.Path)
			}

		case "added":
			content, err := gitClient.ReadFile(ctx, toRef, diff.Path)
			if err != nil {
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

			// Delete old path if it was an artifact
			if diff.OldPath != "" && isMdOld {
				oldContent, err := gitClient.ReadFile(ctx, fromRef, diff.OldPath)
				if err == nil && IsArtifact(oldContent) {
					changeset.Deleted = append(changeset.Deleted, diff.OldPath)
				}
			}

			// Create new path if it's a .md artifact
			if !isMdNew {
				continue
			}
			content, err := gitClient.ReadFile(ctx, toRef, diff.Path)
			if err != nil {
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

			// Check if file was an artifact at fromRef
			wasArtifact := false
			oldContent, oldErr := gitClient.ReadFile(ctx, fromRef, diff.Path)
			if oldErr == nil && IsArtifact(oldContent) {
				wasArtifact = true
			}

			content, err := gitClient.ReadFile(ctx, toRef, diff.Path)
			if err != nil {
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

	files, err := gitClient.ListFiles(ctx, ref, "")
	if err != nil {
		return nil, err
	}

	prefix := ""
	if dir != "/" {
		prefix = dir + "/"
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
