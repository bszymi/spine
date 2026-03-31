package engine

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bszymi/spine/internal/domain"
)

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

const maxSlugLength = 60

// generateBranchName builds a human-readable Git branch name from the
// run mode, artifact identity, and run ID.
//
// Format:
//   - Planning runs: spine/plan/<id>-<slug>
//   - Standard runs: spine/run/<id>-<slug>
func generateBranchName(mode domain.RunMode, artifactID, artifactPath, runID string) string {
	prefix := "spine/run/"
	if mode == domain.RunModePlanning {
		prefix = "spine/plan/"
	}

	slug := slugFromPath(artifactPath)
	id := strings.ToLower(artifactID)

	var name string
	switch {
	case id == "":
		name = slug
	case slug == "" || slug == id:
		name = id
	default:
		name = id + "-" + slug
	}

	if len(name) > maxSlugLength {
		name = name[:maxSlugLength]
		name = strings.TrimRight(name, "-")
	}

	return prefix + name
}

// generateBranchNameWithSuffix appends the run ID hex suffix for collision
// avoidance. The run ID has format "run-XXXXXXXX"; we extract the hex part.
func generateBranchNameWithSuffix(mode domain.RunMode, artifactID, artifactPath, runID string) string {
	base := generateBranchName(mode, artifactID, artifactPath, runID)
	suffix := runIDSuffix(runID)
	return base + "-" + suffix
}

// slugFromPath derives a URL-safe slug from an artifact path.
// Example: "initiatives/init-001/initiative.md" → "initiative"
// Example: "initiatives/init-001/epics/epic-001/tasks/task-003-git-push.md" → "task-003-git-push"
func slugFromPath(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	return sanitize(name)
}

// sanitize converts a string to a Git-ref-safe slug.
func sanitize(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// runIDSuffix extracts the hex portion from a run ID like "run-0a5d0f6d".
func runIDSuffix(runID string) string {
	if strings.HasPrefix(runID, "run-") {
		return runID[4:]
	}
	return runID
}
