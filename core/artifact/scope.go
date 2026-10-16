package artifact

import (
	"path"
	"strings"

	"github.com/bszymi/spine/core/domain"
)

// PathScope is the single classifier of which repository paths are
// governed Markdown artifacts and under which key (INIT-023 TASK-011,
// audit P3-23; moved here from adapters/projection by TASK-017 so the
// governed-merge status lifecycle in core/engine judges exactly the paths
// the projection treats as artifacts, keyed the same way, without an
// adapters/projection import).
//
// A path is in scope when it sits under <dir>/ (any path when dir is the
// repository root), ends in ".md" and is not under templates/ relative to
// the scope; its key is the path with the prefix stripped — the
// artifact-relative path the projection stores and artifact.Service reads.
// The zero value is the repository root.
type PathScope struct {
	dir    string // "" for the repository root
	prefix string // dir + "/", or "" for the root
}

// NewPathScope normalises artifacts_dir exactly like
// service/internal/config.normalizePath: surrounding whitespace is
// trimmed; "", ".", "./" and "/" mean the root; otherwise one leading
// "./" and one trailing "/" are trimmed.
func NewPathScope(artifactsDir string) PathScope {
	d := strings.TrimSpace(artifactsDir)
	if d == "" || d == "." || d == "./" || d == "/" {
		return PathScope{}
	}
	d = strings.TrimPrefix(d, "./")
	d = strings.TrimSuffix(d, "/")
	if d == "" {
		return PathScope{}
	}
	return PathScope{dir: d, prefix: d + "/"}
}

// Dir is the normalised artifacts directory, "" for the repository root.
func (p PathScope) Dir() string { return p.dir }

// ArtifactKey maps a repository path to its projection key, mirroring
// DiscoverAll exactly: the path must sit under <dir>/ (any path when dir
// is the root), end in ".md" and not be under templates/ relative to the
// scope. The key is the path with the prefix stripped.
func (p PathScope) ArtifactKey(repoPath string) (string, bool) {
	rel := repoPath
	if p.prefix != "" {
		if !strings.HasPrefix(repoPath, p.prefix) {
			return "", false
		}
		rel = strings.TrimPrefix(repoPath, p.prefix)
	}
	if rel == "" || !strings.HasSuffix(rel, ".md") {
		return "", false
	}
	if IsTemplatePath(rel) {
		return "", false
	}
	return rel, true
}

// GovernedArtifactKey is ArtifactKey restricted to paths the projection
// can store: a path domain.UnsafeRepositoryPath refuses (a control
// character, U+2028/U+2029, a bidi control, invalid UTF-8) is refused by
// discovery without being read (INIT-023 TASK-003), so it is never a
// governed artifact.
func (p PathScope) GovernedArtifactKey(repoPath string) (string, bool) {
	if domain.UnsafeRepositoryPath(repoPath) {
		return "", false
	}
	return p.ArtifactKey(repoPath)
}

// RepoPath maps an artifact-relative path (a run's TaskPath, a projection
// key) to its repository path the way artifact.Service.Read resolves it:
// one leading "/" is dropped and the path is joined under dir — and,
// lexically cleaned, in the form git names it in a diff. Read joins under
// a subdirectory with filepath.Join, which cleans, but at the repository
// root it passes "./tasks/t.md" or "tasks//t.md" to git as given, and git
// reads them as "tasks/t.md"; the governed-merge lifecycle compares this
// path with diff paths and writes it with CommitFilesOnto, which refuses a
// "." component (codex review, TASK-017 round 1). "" stays "".
func (p PathScope) RepoPath(artifactPath string) string {
	artifactPath = strings.TrimPrefix(artifactPath, "/")
	if artifactPath == "" {
		return ""
	}
	if p.dir == "" {
		return path.Clean(artifactPath)
	}
	return path.Join(p.dir, artifactPath)
}
