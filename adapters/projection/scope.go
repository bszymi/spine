package projection

import (
	"strings"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
)

// workflowsSubdir is the directory, relative to the repository root or
// to artifacts_dir, whose direct *.yaml / *.yml children are workflow
// definitions (architecture/workflow-definition-format.md).
const workflowsSubdir = "workflows/"

// pathScope is the single path classifier both projection entry points
// use (INIT-023 TASK-011, audit P3-23). Before it, FullRebuild scoped
// artifact discovery to artifacts_dir and stripped the prefix, while
// IncrementalSync ran discovery over the whole repository diff and only
// trimmed the prefix — so an out-of-tree file could overwrite a governed
// artifact's row, governed <dir>/workflows/ edits were ignored, and
// templates/ were projected incrementally only. One predicate for both
// paths restores the data-model guarantee that a full rebuild equals an
// incrementally maintained store (architecture/data-model.md §4.1).
type pathScope struct {
	dir    string // "" for the repository root
	prefix string // dir + "/", or "" for the root
}

// newPathScope normalises artifacts_dir exactly like
// service/internal/config.normalizePath: surrounding whitespace is
// trimmed; "", ".", "./" and "/" mean the root; otherwise one leading
// "./" and one trailing "/" are trimmed.
func newPathScope(artifactsDir string) pathScope {
	d := strings.TrimSpace(artifactsDir)
	if d == "" || d == "." || d == "./" || d == "/" {
		return pathScope{}
	}
	d = strings.TrimPrefix(d, "./")
	d = strings.TrimSuffix(d, "/")
	if d == "" {
		return pathScope{}
	}
	return pathScope{dir: d, prefix: d + "/"}
}

// artifactKey maps a repository path to its projection key, mirroring
// artifact.DiscoverAll exactly: the path must sit under <dir>/ (any path
// when dir is the root), end in ".md" and not be under templates/
// relative to the scope. The key is the path with the prefix stripped.
func (p pathScope) artifactKey(repoPath string) (string, bool) {
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
	if artifact.IsTemplatePath(rel) {
		return "", false
	}
	return rel, true
}

// isWorkflow reports whether a repository path is a workflow definition
// the projection keeps a row for: a DIRECT child of the root workflows/
// directory ending in .yaml or .yml, or — when artifacts_dir is a
// subdirectory — a direct child of <dir>/workflows/ (owner question 3:
// both are canonical; workflow.Service and planning runs write the root,
// init-repo seeds the subdirectory). Nested files such as SMP's runner
// step profiles under workflows/steps/ are not definitions: the
// documented location is /workflows/<workflow-id>.yaml and
// workflow.Service can only write a top-level file. artifact.IsWorkflowPath
// (any depth) is deliberately left unchanged for its other callers.
func (p pathScope) isWorkflow(repoPath string) bool {
	if isDirectWorkflowChild(repoPath, workflowsSubdir) {
		return true
	}
	return p.prefix != "" && isDirectWorkflowChild(repoPath, p.prefix+workflowsSubdir)
}

func isDirectWorkflowChild(path, dir string) bool {
	if !strings.HasPrefix(path, dir) {
		return false
	}
	name := path[len(dir):]
	if name == "" || strings.Contains(name, "/") {
		return false
	}
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// artifactDiffs keeps the diffs whose path is an artifact candidate in
// scope (artifactKey's whole predicate, applied to both sides of a
// rename). A rename that crosses the scope boundary — into or out of
// <dir>/, or across templates/ inside it — becomes:
//   - "added" (new path) when only the new path is in scope;
//   - "deleted" (old path) when only the old path is in scope.
//
// Without the conversion, discovery would read a template as an
// artifact, or keep a stale row for a file that left the scope.
func (p pathScope) artifactDiffs(diffs []git.FileDiff) []git.FileDiff {
	out := make([]git.FileDiff, 0, len(diffs))
	for _, d := range diffs {
		_, newIn := p.artifactKey(d.Path)
		if d.Status != "renamed" {
			if newIn {
				out = append(out, d)
			}
			continue
		}
		oldIn := false
		if d.OldPath != "" {
			_, oldIn = p.artifactKey(d.OldPath)
		}
		switch {
		case newIn && oldIn:
			out = append(out, d)
		case newIn:
			out = append(out, git.FileDiff{Path: d.Path, Status: "added"})
		case oldIn:
			out = append(out, git.FileDiff{Path: d.OldPath, Status: "deleted"})
		}
	}
	return out
}

// WorkflowDirs returns the repository-relative directories whose direct
// *.yaml / *.yml children the projection treats as workflow definitions:
// "workflows/", plus "<dir>/workflows/" when artifactsDir names a
// subdirectory (normalised like .spine.yaml's artifacts_dir). A
// governance-path guard (INIT-023 TASK-006) uses it so every directory
// the projection installs workflows from is guarded.
func WorkflowDirs(artifactsDir string) []string {
	p := newPathScope(artifactsDir)
	if p.prefix == "" {
		return []string{workflowsSubdir}
	}
	return []string{workflowsSubdir, p.prefix + workflowsSubdir}
}
