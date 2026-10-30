package git

import (
	"context"
	"strings"
	"time"
)

// GitClient defines the interface for Git repository operations.
// Per Implementation Guide §3.1.
// The v0.x implementation shells out to the git CLI.
type GitClient interface {
	Clone(ctx context.Context, url, path string) error
	Commit(ctx context.Context, opts CommitOpts) (CommitResult, error)
	Merge(ctx context.Context, opts MergeOpts) (MergeResult, error)
	CreateBranch(ctx context.Context, name, base string) error
	DeleteBranch(ctx context.Context, name string) error
	Diff(ctx context.Context, from, to string) ([]FileDiff, error)
	MergeBase(ctx context.Context, a, b string) (string, error)
	Log(ctx context.Context, opts LogOpts) ([]CommitInfo, error)
	ReadFile(ctx context.Context, ref, path string) ([]byte, error)
	ListFiles(ctx context.Context, ref, pattern string) ([]string, error)
	Head(ctx context.Context) (string, error)
	// RefSHA resolves a named ref (branch, tag, or HEAD) to its commit
	// SHA. Equivalent to `git rev-parse <ref>`. Used to capture the SHA
	// of a specific branch independent of the current working-tree HEAD,
	// e.g. when recording the cut-from baseline of a newly-created
	// branch whose `base` argument is a branch name rather than HEAD.
	RefSHA(ctx context.Context, ref string) (string, error)
	Push(ctx context.Context, remote, ref string) error
	PushBranch(ctx context.Context, remote, branch string) error
	DeleteRemoteBranch(ctx context.Context, remote, branch string) error
}

// PathFilterLister is the optional listing capability (INIT-024
// TASK-004, review finding N-6): ListFilesFunc returns the paths at ref
// that keep accepts (every path when keep is nil), with keep applied while
// git lists, so only the kept paths count against the client's listing
// limit. A caller finds it by type assertion on its GitClient and falls
// back to ListFiles, so no interface an embedder implements grows a
// method. *CLIClient implements it, and so does any type that embeds one
// (SMP's gitread.Client).
type PathFilterLister interface {
	ListFilesFunc(ctx context.Context, ref string, keep func(path string) bool) ([]string, error)
	// ListFilesUnder is ListFilesFunc limited to the paths under dir
	// (review round 1): git walks only that subtree.
	ListFilesUnder(ctx context.Context, ref, dir string, keep func(path string) bool) ([]string, error)
}

// FileLister is the one GitClient method ListFilesKeeping needs.
type FileLister interface {
	ListFiles(ctx context.Context, ref, pattern string) ([]string, error)
}

// ListFilesKeeping lists, in git's order, the paths at ref that keep
// accepts (every path when keep is nil), under dir when dir is not ""
// (a repository-relative directory: only paths beginning "<dir>/"). With
// a PathFilterLister it lists through ListFilesFunc / ListFilesUnder, so
// only the kept paths count against the client's limit and git walks only
// dir's subtree; otherwise through ListFiles(ref, "") filtered here —
// exactly what every caller did before the capability, with the client's
// limit then applying to the whole listing. keep should enforce any
// prefix it relies on itself: dir only narrows what git walks.
func ListFilesKeeping(ctx context.Context, gc FileLister, ref, dir string, keep func(path string) bool) ([]string, error) {
	if l, ok := gc.(PathFilterLister); ok {
		if dir != "" {
			return l.ListFilesUnder(ctx, ref, dir, keep)
		}
		return l.ListFilesFunc(ctx, ref, keep)
	}
	files, err := gc.ListFiles(ctx, ref, "")
	if err != nil {
		return nil, err
	}
	prefix := ""
	if d := strings.TrimSuffix(dir, "/"); d != "" && d != "." {
		prefix = d + "/"
	}
	if keep == nil && prefix == "" {
		return files, nil
	}
	var kept []string
	for _, f := range files {
		if strings.HasPrefix(f, prefix) && (keep == nil || keep(f)) {
			kept = append(kept, f)
		}
	}
	return kept, nil
}

// CommitOpts defines options for creating a Git commit.
type CommitOpts struct {
	Message    string            // Commit summary line
	Body       string            // Optional commit body
	Trailers   map[string]string // Structured trailers (Trace-ID, Actor-ID, Run-ID, Operation)
	Author     Author            // Commit author identity
	AllowEmpty bool              // Allow creating empty commits
}

// Author represents a Git commit author.
type Author struct {
	Name  string
	Email string
}

// CommitResult contains the result of a successful commit.
type CommitResult struct {
	SHA string // Full commit SHA
}

// MergeOpts defines options for merging branches.
type MergeOpts struct {
	Source string // Branch to merge from
	// SourceSHA, when set, is the exact commit to merge (a full lowercase
	// hex object id); Source is then used only for messages. Callers
	// that leave it empty get the branch-name merge (INIT-023 TASK-005).
	SourceSHA string
	// Target is the branch to merge into. Required (INIT-023 TASK-012):
	// CLIClient refuses an empty Target, except for the abort sentinel
	// (Strategy "abort" or Source "--abort"), which must have none.
	Target   string
	Strategy string // "fast-forward" or "merge-commit"
	Message  string // Merge commit message (for merge-commit strategy)
	Trailers map[string]string
}

// MergeResult contains the result of a successful merge.
type MergeResult struct {
	SHA string // Resulting commit SHA
	// BaseSHA is the target tip the merge was computed on (set by
	// SHA-bound merges, INIT-023 TASK-005; empty otherwise).
	BaseSHA     string
	FastForward bool // True if merge was fast-forward
}

// LogOpts defines options for querying Git log.
type LogOpts struct {
	Path  string // Filter by file path (optional)
	Limit int    // Max number of commits (0 = unlimited)
	Since string // Commit SHA to start from (exclusive)
}

// CommitInfo represents a Git commit from the log.
type CommitInfo struct {
	SHA       string
	Author    Author
	Message   string
	Trailers  map[string]string
	Timestamp time.Time
}

// FileDiff represents a file change between two Git refs.
type FileDiff struct {
	Path    string
	OldPath string // set for renames (the previous path)
	Status  string // "added", "modified", "deleted", "renamed"
}
