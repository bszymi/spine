package artifact

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
)

// idPrefixes maps artifact types to their ID prefix strings.
var idPrefixes = map[domain.ArtifactType]string{
	domain.ArtifactTypeInitiative: "INIT",
	domain.ArtifactTypeEpic:      "EPIC",
	domain.ArtifactTypeTask:      "TASK",
	domain.ArtifactTypeADR:       "ADR",
}

// idPadding maps artifact types to their zero-padding width.
var idPadding = map[domain.ArtifactType]int{
	domain.ArtifactTypeInitiative: 3,
	domain.ArtifactTypeEpic:      3,
	domain.ArtifactTypeTask:      3,
	domain.ArtifactTypeADR:       4,
}

// NextID scans the parent directory at the given ref for existing artifacts
// of the specified type and returns the next sequential ID.
//
// Example: NextID(ctx, gitClient, "initiatives/INIT-003/epics/EPIC-003/tasks", "Task", "HEAD")
// Returns: "TASK-006" if TASK-001 through TASK-005 exist.
//
// Rules:
//   - Scans for files/directories matching the artifact type prefix (TASK-, EPIC-, etc.)
//   - Extracts the numeric part from each match
//   - Returns max+1, zero-padded per naming conventions
//   - Gaps are preserved (does not fill gaps)
//   - IDs in the 900-series (follow-up IDs) are excluded from scanning
//   - Returns TYPE-001 (or TYPE-0001 for ADR) if no existing artifacts are found
func NextID(ctx context.Context, gitClient git.GitClient, parentDir string, artifactType domain.ArtifactType, ref string) (string, error) {
	prefix, ok := idPrefixes[artifactType]
	if !ok {
		return "", fmt.Errorf("unsupported artifact type for ID allocation: %s", artifactType)
	}

	padding, ok := idPadding[artifactType]
	if !ok {
		return "", fmt.Errorf("no padding defined for artifact type: %s", artifactType)
	}

	if ref == "" {
		ref = "HEAD"
	}

	files, err := gitClient.ListFiles(ctx, ref, "")
	if err != nil {
		return "", fmt.Errorf("list files at %s: %w", ref, err)
	}

	// Build the pattern to match entries under parentDir.
	// Match prefix followed by dash and digits (e.g., TASK-001, EPIC-002).
	entryPrefix := prefix + "-"
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(entryPrefix) + `(\d+)`)

	// Normalize parentDir for prefix matching.
	scanDir := strings.TrimSuffix(parentDir, "/")
	if scanDir != "" {
		scanDir += "/"
	}

	maxNum := 0
	seen := make(map[string]bool) // deduplicate directory entries

	for _, file := range files {
		// Only consider files under the parent directory.
		if scanDir != "" && !strings.HasPrefix(file, scanDir) {
			continue
		}

		// Extract the first path component after the parent directory.
		rel := strings.TrimPrefix(file, scanDir)
		entry := strings.SplitN(rel, "/", 2)[0]

		if seen[entry] {
			continue
		}
		seen[entry] = true

		matches := pattern.FindStringSubmatch(entry)
		if matches == nil {
			continue
		}

		var num int
		if _, err := fmt.Sscanf(matches[1], "%d", &num); err != nil {
			continue
		}

		// Exclude 900-series follow-up IDs from regular allocation.
		if num >= 900 {
			continue
		}

		if num > maxNum {
			maxNum = num
		}
	}

	nextNum := maxNum + 1
	return fmt.Sprintf("%s-%0*d", prefix, padding, nextNum), nil
}

// Slugify converts a title string to a valid artifact slug.
//
// Rules: lowercase, replace spaces/underscores with hyphens, strip non-alphanumeric
// (except hyphens), collapse consecutive hyphens, trim leading/trailing hyphens.
func Slugify(title string) string {
	s := strings.ToLower(title)

	// Replace spaces and underscores with hyphens.
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '_' {
			return '-'
		}
		return r
	}, s)

	// Strip non-alphanumeric characters (keep hyphens).
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			b.WriteRune(r)
		}
	}
	s = b.String()

	// Collapse consecutive hyphens.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}

	// Trim leading/trailing hyphens.
	s = strings.Trim(s, "-")

	return s
}

// BuildArtifactPath constructs the full path for a new artifact.
//
//   - Task: parentDir/TASK-XXX-slug.md (file)
//   - Epic: parentDir/EPIC-XXX-slug/epic.md (directory + file)
//   - Initiative: parentDir/INIT-XXX-slug/initiative.md (directory + file)
//   - ADR: parentDir/ADR-XXXX-slug.md (file)
func BuildArtifactPath(artifactType domain.ArtifactType, id, slug, parentDir string) string {
	dirSlug := strings.ToLower(id) + "-" + slug
	parent := strings.TrimSuffix(parentDir, "/")

	switch artifactType {
	case domain.ArtifactTypeTask:
		// Tasks are files: parentDir/TASK-XXX-slug.md
		return filepath.Join(parent, dirSlug+".md")
	case domain.ArtifactTypeEpic:
		// Epics are directories: parentDir/EPIC-XXX-slug/epic.md
		return filepath.Join(parent, dirSlug, "epic.md")
	case domain.ArtifactTypeInitiative:
		// Initiatives are directories: parentDir/INIT-XXX-slug/initiative.md
		return filepath.Join(parent, dirSlug, "initiative.md")
	case domain.ArtifactTypeADR:
		// ADRs are files: parentDir/ADR-XXXX-slug.md
		return filepath.Join(parent, dirSlug+".md")
	default:
		// Document types (Governance, Architecture, Product): parentDir/slug.md
		return filepath.Join(parent, slug+".md")
	}
}
